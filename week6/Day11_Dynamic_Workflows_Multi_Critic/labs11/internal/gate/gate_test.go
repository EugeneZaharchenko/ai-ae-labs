package gate

import (
	"context"
	"strings"
	"testing"
	"time"
)

const okTest = "package billing\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tif !F() {\n\t\tt.Fatal(\"F() = false\")\n\t}\n}\n"

func TestGradeStages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		code      string
		test      string
		wantOK    bool
		wantStage string
		wantOut   string
	}{
		{name: "green", code: "package billing\nfunc F() bool { return true }\n", test: okTest, wantOK: true},
		{name: "syntax error stops at parse", code: "package billing\nfunc F( {", test: okTest, wantStage: StageParse},
		{
			name:      "forbidden import stops at policy",
			code:      "package billing\nimport \"os/exec\"\nvar _ = exec.Command\nfunc F() bool { return true }\n",
			test:      okTest,
			wantStage: StagePolicy,
			wantOut:   "os/exec",
		},
		{
			name:      "type error stops at build",
			code:      "package billing\nfunc F() bool { return 1 }\n",
			test:      okTest,
			wantStage: StageBuild,
			wantOut:   "cannot use 1",
		},
		{
			name:      "vet finding stops at vet",
			code:      "package billing\nimport \"fmt\"\nfunc F() bool { fmt.Printf(\"%d\", \"x\"); return true }\n",
			test:      okTest,
			wantStage: StageVet,
			wantOut:   "Printf",
		},
		{
			name:      "failing test stops at test",
			code:      "package billing\nfunc F() bool { return false }\n",
			test:      okTest,
			wantStage: StageTest,
			wantOut:   "F() = false",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rep := Runner{}.Grade(context.Background(), Spec{Code: tt.code, Test: tt.test})
			if rep.OK != tt.wantOK || rep.Stage != tt.wantStage {
				t.Fatalf("OK=%v Stage=%q, want OK=%v Stage=%q\n%s", rep.OK, rep.Stage, tt.wantOK, tt.wantStage, rep.Output)
			}
			if !strings.Contains(rep.Output, tt.wantOut) {
				t.Errorf("Output = %q, want it to contain %q", rep.Output, tt.wantOut)
			}
			if rep.Elapsed <= 0 {
				t.Error("Elapsed not set")
			}
		})
	}
}

// TestGradeKillsRunawayCandidate pins the deadline: `for {}` in a test costs
// one stage timeout, and the report says so instead of hanging the loop.
func TestGradeKillsRunawayCandidate(t *testing.T) {
	t.Parallel()
	code := "package billing\nfunc F() bool { for {} }\n"
	start := time.Now()
	rep := Runner{StageTimeout: 3 * time.Second}.Grade(context.Background(), Spec{Code: code, Test: okTest})
	if rep.OK || rep.Stage != StageTest {
		t.Fatalf("OK=%v Stage=%q, want a test-stage failure", rep.OK, rep.Stage)
	}
	if !strings.Contains(rep.Output, "timeout") {
		t.Errorf("Output = %q, want a timeout marker", rep.Output)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("took %v, the deadline did not fire", took)
	}
}

// TestGradeScrubsEnvironment pins that the parent's secrets do not reach the
// candidate's toolchain.
func TestGradeScrubsEnvironment(t *testing.T) {
	t.Setenv("LAB11_SECRET", "must-not-leak")
	test := "package billing\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestEnv(t *testing.T) {\n\tif v := os.Getenv(\"LAB11_SECRET\"); v != \"\" {\n\t\tt.Fatalf(\"leaked: %q\", v)\n\t}\n}\n"
	rep := Runner{}.Grade(context.Background(), Spec{Code: "package billing\n", Test: test})
	if !rep.OK {
		t.Fatalf("stage %s: %s", rep.Stage, rep.Output)
	}
}

func TestGradeMissingToolchain(t *testing.T) {
	t.Parallel()
	rep := Runner{Go: "/nonexistent/go"}.Grade(context.Background(), Spec{Code: "package billing\n", Test: okTest})
	if rep.OK || rep.Stage != StageBuild {
		t.Fatalf("OK=%v Stage=%q, want a build-stage failure", rep.OK, rep.Stage)
	}
}

func TestGradeHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := Runner{}.Grade(ctx, Spec{Code: "package billing\nfunc F() bool { return true }\n", Test: okTest})
	if rep.OK {
		t.Fatal("a cancelled context must not produce a green report")
	}
}

func TestCheckImports(t *testing.T) {
	t.Parallel()
	if got := CheckImports("package p\nimport \"strings\"\n"); got != "" {
		t.Errorf("strings flagged: %q", got)
	}
	if got := CheckImports("package p\nimport (\n\t\"net/http\"\n)\n"); !strings.Contains(got, "net/http") {
		t.Errorf("net/http not flagged: %q", got)
	}
	if got := CheckImports("not go"); got != "" {
		t.Errorf("unparsable source must be left to the parse stage, got %q", got)
	}
}

func TestClipKeepsHeadAndTail(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 3000) + "TAIL"
	got := clip(long)
	if len(got) >= len(long) || !strings.HasSuffix(got, "TAIL") || !strings.Contains(got, "обрізано") {
		t.Errorf("clip: len=%d suffix ok=%v", len(got), strings.HasSuffix(got, "TAIL"))
	}
	if clip("  short \n") != "short" {
		t.Error("short output must only be trimmed")
	}
}
