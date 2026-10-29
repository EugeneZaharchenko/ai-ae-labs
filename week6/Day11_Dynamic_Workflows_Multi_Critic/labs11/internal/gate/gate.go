// Package gate is the objective, model-free grader of the Day 11 lab: it takes
// the source a maker model produced and runs it through the same checks a human
// reviewer's CI would — parse, import policy, `go build`, `go vet`, `go test` —
// inside a throwaway module with a scrubbed environment and a hard timeout.
//
// Why this is a package of its own and not a few lines of os/exec in a node:
// the code under test is LLM-generated, i.e. untrusted input. Three decisions
// are made here once, where they can be tested, instead of being re-invented
// (or forgotten) at every call site:
//
//   - the maker owns exactly one file (candidate.go); the tests are ours and are
//     written next to it, so a model cannot "fix" a failing test by editing it;
//   - the child process never sees the parent's environment — an API key in
//     apps/.env must not be readable by a generated `os.Getenv` in a test;
//   - every stage has a deadline and a process-group kill, so `for {}` in a
//     candidate costs one stage timeout, not the invocation.
//
// This is process-level hygiene, NOT a sandbox: generated code can still read
// the filesystem. Production runs belong in a microVM/gVisor sandbox (week 7
// of the platform track); the gate marks where that boundary plugs in.
package gate

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

// Stage names, in execution order. A Report names the first stage that failed.
const (
	StageParse  = "parse"
	StagePolicy = "policy"
	StageBuild  = "build"
	StageVet    = "vet"
	StageTest   = "test"
)

// maxOutput bounds the compiler/test output copied into a Report. The output is
// fed back to the maker as feedback; an unbounded panic trace would spend the
// token budget on noise.
const maxOutput = 2000

// forbiddenImports are packages a unit-level candidate has no business using.
// It is a tripwire for prompt injection, not a security boundary.
var forbiddenImports = map[string]bool{
	"os/exec": true, "net": true, "net/http": true, "syscall": true,
	"unsafe": true, "plugin": true, "os/signal": true, "runtime/debug": true,
}

// Spec is one grading request.
type Spec struct {
	// Code is the candidate's source (package billing).
	Code string
	// Test is the test file the candidate must satisfy.
	Test string
}

// Report is the verdict of one grading run.
type Report struct {
	OK bool
	// Stage is the first stage that failed; empty when OK.
	Stage string
	// Output is the bounded tool output of the failing stage.
	Output string
	// Formatted is the gofmt-normalised candidate (set when parsing succeeded).
	Formatted string
	// Elapsed is the wall time of the whole run.
	Elapsed time.Duration
}

// Runner grades candidates. The zero value is usable.
type Runner struct {
	// StageTimeout bounds each exec stage. Zero means 30s.
	StageTimeout time.Duration
	// Go is the go binary. Empty means the one found on PATH.
	Go string
}

func (r Runner) stageTimeout() time.Duration {
	if r.StageTimeout > 0 {
		return r.StageTimeout
	}
	return 30 * time.Second
}

// Grade runs every stage and stops at the first failure.
func (r Runner) Grade(ctx context.Context, s Spec) Report {
	start := time.Now()
	rep := r.grade(ctx, s)
	rep.Elapsed = time.Since(start)
	return rep
}

func (r Runner) grade(ctx context.Context, s Spec) Report {
	formatted, err := format.Source([]byte(s.Code))
	if err != nil {
		return Report{Stage: StageParse, Output: clip(err.Error())}
	}
	rep := Report{Formatted: string(formatted)}

	if bad := CheckImports(rep.Formatted); bad != "" {
		rep.Stage, rep.Output = StagePolicy, bad
		return rep
	}

	dir, err := os.MkdirTemp("", "lab11-gate-*")
	if err != nil {
		rep.Stage, rep.Output = StageBuild, "gate: "+err.Error()
		return rep
	}
	defer os.RemoveAll(dir)

	files := map[string]string{
		"go.mod":            "module ledgerworks.example/candidate\n\ngo 1.24\n",
		"candidate.go":      rep.Formatted,
		"candidate_test.go": s.Test,
	}
	for name, body := range files {
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
		if out, err := r.run(ctx, dir, st.args...); err != nil {
			rep.Stage, rep.Output = st.name, clip(out)
			return rep
		}
	}
	rep.OK = true
	return rep
}

// CheckImports returns a human-readable violation for the first forbidden
// import in src, or "" when the imports are acceptable.
func CheckImports(src string) string {
	f, err := parser.ParseFile(token.NewFileSet(), "candidate.go", src, parser.ImportsOnly)
	if err != nil {
		return ""
	}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err == nil && forbiddenImports[path] {
			return fmt.Sprintf("заборонений імпорт %q: кандидат не має права на процеси, мережу чи unsafe", path)
		}
	}
	return ""
}

// ErrTimeout marks a stage killed by its deadline.
var ErrTimeout = errors.New("gate: stage timeout")

func (r Runner) run(ctx context.Context, dir string, args ...string) (string, error) {
	goBin := r.Go
	if goBin == "" {
		p, err := exec.LookPath("go")
		if err != nil {
			return "", fmt.Errorf("gate: go toolchain not found: %w", err)
		}
		goBin = p
	}
	ctx, cancel := context.WithTimeout(ctx, r.stageTimeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	cmd.WaitDelay = 2 * time.Second
	isolate(cmd)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf

	err := cmd.Run()
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return buf.String() + "\n[timeout]", ErrTimeout
	}
	return buf.String(), err
}

// scrubbedEnv is the whole environment the candidate's toolchain gets. Nothing
// is inherited by default: add a variable here only with a reason.
func scrubbedEnv() []string {
	env := []string{
		"GOTOOLCHAIN=local", "GOPROXY=off", "GOWORK=off", "GOFLAGS=-mod=mod",
		"CGO_ENABLED=0", "GOSUMDB=off",
	}
	for _, k := range []string{"PATH", "HOME", "GOCACHE", "GOROOT", "TMPDIR", "SYSTEMROOT", "LOCALAPPDATA"} {
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
	return s[:half] + "\n…[обрізано]…\n" + s[len(s)-half:]
}
