package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The deterministic gate: the objective critic that runs BEFORE any model critic.
//
// A candidate written by a model is untrusted input, and the cheapest, most
// reliable reviewer of Go code is the Go toolchain: parse, import policy, build,
// vet, test. A panel of LLM critics that opens its mouth about code that does not
// compile is paying for opinions about a build failure. So the loop asks the
// toolchain first, feeds its output back to the maker, and only spends critic
// tokens on code that already builds and passes the visible tests.
//
// This is a compact copy of the labs' internal/gate, which adds a process-group
// kill and is graded. It is process-level hygiene, NOT a sandbox: generated code
// can still read the filesystem. Production runs belong in a microVM or gVisor
// sandbox (the platform track); this function marks where that boundary plugs in.

// Stage names, in execution order. A Report names the first stage that failed.
const (
	StageParse  = "parse"
	StagePolicy = "policy"
	StageBuild  = "build"
	StageVet    = "vet"
	StageTest   = "test"
)

// maxOutput bounds the toolchain output copied into a Report. The output goes
// back to the maker as feedback; an unbounded panic trace spends the token
// budget on noise.
const maxOutput = 1500

// forbiddenImports are packages a unit-level candidate has no business using. A
// tripwire for prompt injection ("also read $HOME"), not a security boundary.
var forbiddenImports = map[string]bool{
	"os/exec": true, "net": true, "net/http": true, "syscall": true,
	"unsafe": true, "plugin": true, "os/signal": true,
}

// Report is the verdict of one grading run.
type Report struct {
	OK     bool
	Stage  string // first stage that failed; empty when OK
	Output string // bounded tool output of the failing stage
	Code   string // gofmt-normalised candidate, set when parsing succeeded
}

// Line is the one-line form used in the attempt trace.
func (r Report) Line() string {
	if r.OK {
		return "gate: pass (parse, policy, build, vet, test)"
	}
	return fmt.Sprintf("gate: FAIL at %s", r.Stage)
}

// Grade runs every stage against test and stops at the first failure. timeout
// bounds each exec stage, so `for {}` in a candidate costs one timeout.
func Grade(ctx context.Context, code, test string, timeout time.Duration) Report {
	formatted, err := format.Source([]byte(code))
	if err != nil {
		return Report{Stage: StageParse, Output: clip(err.Error())}
	}
	rep := Report{Code: string(formatted)}

	if bad := checkImports(rep.Code); bad != "" {
		rep.Stage, rep.Output = StagePolicy, bad
		return rep
	}

	dir, err := os.MkdirTemp("", "week6-gate-*")
	if err != nil {
		rep.Stage, rep.Output = StageBuild, "gate: "+err.Error()
		return rep
	}
	defer os.RemoveAll(dir)

	// The maker owns exactly one file. The tests are ours, written next to it, so
	// a model cannot "fix" a failing test by editing it.
	for name, body := range map[string]string{
		"go.mod":            "module ledgerworks.example/candidate\n\ngo 1.24\n",
		"candidate.go":      rep.Code,
		"candidate_test.go": test,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			rep.Stage, rep.Output = StageBuild, "gate: "+err.Error()
			return rep
		}
	}

	for _, st := range []struct {
		name string
		args []string
	}{
		{StageBuild, []string{"build", "./..."}},
		{StageVet, []string{"vet", "./..."}},
		{StageTest, []string{"test", "-count=1", "-timeout", "20s", "./..."}},
	} {
		if out, err := run(ctx, dir, timeout, st.args...); err != nil {
			rep.Stage, rep.Output = st.name, clip(out)
			return rep
		}
	}
	rep.OK = true
	return rep
}

// checkImports returns a description of the first forbidden import, or "".
func checkImports(src string) string {
	f, err := parser.ParseFile(token.NewFileSet(), "candidate.go", src, parser.ImportsOnly)
	if err != nil {
		return ""
	}
	for _, imp := range f.Imports {
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && forbiddenImports[path] {
			return fmt.Sprintf("forbidden import %q: a candidate has no business with processes, the network or unsafe", path)
		}
	}
	return ""
}

func run(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("gate: go toolchain not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf

	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return buf.String() + "\n[timeout]", ctx.Err()
	}
	return buf.String(), err
}

// scrubbedEnv is the whole environment the candidate's toolchain gets. Nothing
// is inherited by default: an API key in the parent's environment must not be
// readable by a generated `os.Getenv` inside a test.
func scrubbedEnv() []string {
	env := []string{"GOTOOLCHAIN=local", "GOPROXY=off", "GOWORK=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=0", "GOSUMDB=off"}
	for _, k := range []string{"PATH", "HOME", "GOCACHE", "GOROOT", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxOutput {
		return s
	}
	half := maxOutput / 2
	return s[:half] + "\n…[clipped]…\n" + s[len(s)-half:]
}
