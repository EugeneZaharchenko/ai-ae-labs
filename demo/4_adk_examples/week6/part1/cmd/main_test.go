package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Two contours. The pure layer — verdict schema, merge policy, the loop's state
// machine over fake steps, the gate against the real toolchain — needs no ADK.
// The CLI runs are end-to-end through a real workflow agent with the offline
// stand-in, and pin what only a run can show: that the second attempt comes from
// FEEDBACK, and that the gate alone accepts the bug.

const gateTimeout = 60 * time.Second

func TestParseVerdict(t *testing.T) {
	good := `Here is my verdict:
` + "```json\n" + `{"critic":"correctness","passed":false,"comments":["overflow"]}` + "\n```\nHope it helps!"
	v, err := ParseVerdict(good, RoleCorrectness)
	if err != nil || v.Passed || len(v.Comments) != 1 {
		t.Fatalf("fenced JSON with prose around it must parse: %+v, %v", v, err)
	}

	for name, raw := range map[string]string{
		"prose only":       "looks fine to me",
		"wrong role":       `{"critic":"style","passed":true}`,
		"fail no comments": `{"critic":"correctness","passed":false}`,
		"not json":         `{"critic": correctness}`,
	} {
		if _, err := ParseVerdict(raw, RoleCorrectness); !errors.Is(err, ErrInvalidVerdict) {
			t.Errorf("%s: err = %v, want ErrInvalidVerdict", name, err)
		}
	}
}

func TestMergeFailsClosedOnTheBlockingRole(t *testing.T) {
	pass := func(r Role) Verdict { return Verdict{Critic: r, Passed: true} }
	bad := errors.New("garbage")

	// Correctness silent, the others approve: that is not an approval.
	d := Merge(map[Role]Verdict{RoleStyle: pass(RoleStyle), RolePerformance: pass(RolePerformance)},
		map[Role]error{RoleCorrectness: bad})
	if d.Accept || len(d.Blocking) != 1 {
		t.Errorf("an unusable correctness verdict must block: %+v", d)
	}

	// Style fails: advisory only, never blocks.
	d = Merge(map[Role]Verdict{
		RoleCorrectness: pass(RoleCorrectness),
		RoleStyle:       {Critic: RoleStyle, Comments: []string{"rename x"}},
		RolePerformance: pass(RolePerformance),
	}, nil)
	if !d.Accept || len(d.Advisory) != 1 {
		t.Errorf("a failing advisory critic must not block: %+v", d)
	}
}

func TestGateStages(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name, code, stage, want string
	}{
		{"reference is green", referenceCandidate, "", ""},
		{"syntax error stops at parse", "package billing\nfunc F( {", StageParse, ""},
		{"forbidden import stops at policy", "package billing\nimport \"os/exec\"\nvar _ = exec.Command\n", StagePolicy, "os/exec"},
		{"type error stops at build", "package billing\nfunc ProrateRefund() int { return \"x\" }\n", StageBuild, "cannot use"},
		{"visible tests catch a wrong answer", strings.Replace(referenceCandidate, "num.Lsh(num, 1)", "", 1), StageTest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := Grade(ctx, tt.code, visibleTests, gateTimeout)
			if (tt.stage == "") != rep.OK || rep.Stage != tt.stage {
				t.Fatalf("OK=%v Stage=%q, want stage %q\n%s", rep.OK, rep.Stage, tt.stage, rep.Output)
			}
			if !strings.Contains(rep.Output, tt.want) {
				t.Errorf("output %q does not contain %q", rep.Output, tt.want)
			}
		})
	}
}

// TestTaskDataIsValid grades the task's own fixtures with the real toolchain: it
// is the example's test-validity check. A reference that fails, or a buggy variant
// that survives the holdout, makes every line the CLI prints meaningless.
func TestTaskDataIsValid(t *testing.T) {
	ctx := context.Background()
	if rep := Grade(ctx, referenceCandidate, visibleTests, gateTimeout); !rep.OK {
		t.Errorf("reference vs visible: %s\n%s", rep.Stage, rep.Output)
	}
	if rep := Grade(ctx, referenceCandidate, holdoutTests, gateTimeout); !rep.OK {
		t.Errorf("reference vs holdout: %s\n%s", rep.Stage, rep.Output)
	}
	if rep := Grade(ctx, buggyCandidate, visibleTests, gateTimeout); !rep.OK {
		t.Errorf("buggy must pass the visible tests, or the gate would catch it and the example teaches nothing: %s\n%s", rep.Stage, rep.Output)
	}
	if rep := Grade(ctx, buggyCandidate, holdoutTests, gateTimeout); rep.OK {
		t.Error("buggy passes the holdout: the holdout does not catch what it exists to catch")
	}
}

func TestGateKillsARunawayCandidate(t *testing.T) {
	code := "package billing\nimport \"errors\"\nvar ErrInvalid = errors.New(\"x\")\nfunc ProrateRefund(int64, int, int) (int64, error) { for {} }\n"
	start := time.Now()
	rep := Grade(context.Background(), code, visibleTests, 3*time.Second)
	if rep.OK || rep.Stage != StageTest {
		t.Fatalf("OK=%v Stage=%q, want a test-stage failure", rep.OK, rep.Stage)
	}
	if time.Since(start) > 40*time.Second {
		t.Errorf("the runaway candidate was not stopped by its deadline: %s", time.Since(start))
	}
}

func TestRunLoopOrdersTheGateBeforeTheCritics(t *testing.T) {
	var critiqued int
	steps := Steps{
		Make: func(_ context.Context, p string) (string, error) {
			if strings.Contains(p, "FEEDBACK") {
				return "good", nil
			}
			return "bad", nil
		},
		Gate: func(_ context.Context, code string) Report {
			if code == "bad" {
				return Report{Stage: StageBuild, Output: "undefined: x"}
			}
			return Report{OK: true, Code: code}
		},
		Critique: func(context.Context, string) (map[Role]string, error) {
			critiqued++
			return map[Role]string{
				RoleCorrectness: `{"critic":"correctness","passed":true}`,
				RoleStyle:       `{"critic":"style","passed":true}`,
				RolePerformance: `{"critic":"performance","passed":true}`,
			}, nil
		},
	}
	res, err := RunLoop(context.Background(), 3, true, "task", steps)
	if err != nil || !res.Accepted || len(res.Attempts) != 2 {
		t.Fatalf("want accepted on attempt 2: %+v, %v", res, err)
	}
	if critiqued != 1 {
		t.Errorf("the panel ran %d times; a candidate that does not build must never reach it", critiqued)
	}
	if !strings.Contains(Feedback("task", res.Attempts[0]), "undefined: x") {
		t.Error("the toolchain's output must travel back to the maker")
	}
}

func TestRunLoopStopsAtTheBudgetAndNamesWhy(t *testing.T) {
	steps := Steps{
		Make: func(context.Context, string) (string, error) { return "bad", nil },
		Gate: func(context.Context, string) Report { return Report{Stage: StageTest, Output: "FAIL"} },
	}
	res, err := RunLoop(context.Background(), 3, true, "task", steps)
	if !errors.Is(err, ErrBudgetExhausted) || res.Accepted || len(res.Attempts) != 3 {
		t.Fatalf("want ErrBudgetExhausted after exactly 3 attempts: %+v, %v", res, err)
	}
	if !strings.Contains(err.Error(), `"test"`) {
		t.Errorf("the error must name the last failing stage: %v", err)
	}
}

func TestCLIOfflineCriticsCatchTheFloat64Bug(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline"}, &out)
	report := out.String()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, report)
	}
	for _, want := range []string{
		"attempt 1\n  gate: pass",
		"correctness ✗  style ✓  performance ✓",
		"bug #423",
		"attempt 2\n  gate: pass",
		"correctness ✓  style ✓  performance ✓",
		"accepted: attempt 2",
		"holdout:  PASS",
		"no usage reported",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q\n%s", want, report)
		}
	}
}

func TestCLIOfflineWithoutCriticsAcceptsTheBug(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline", "-critics=false"}, &out)
	report := out.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1: a loop that accepts a wrong candidate must be visible to a shell\n%s", code, report)
	}
	for _, want := range []string{"critics: off", "accepted: attempt 1", "holdout:  FAIL at test — accepted, and wrong"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q\n%s", want, report)
		}
	}
}

func TestCLIRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{{"-max-attempts", "0"}, {"-nope"}} {
		var out bytes.Buffer
		if code := runCLI(args, &out); code != 2 || out.Len() != 0 {
			t.Errorf("%v: want exit 2 and empty stdout, got %d %q", args, code, out.String())
		}
	}
}
