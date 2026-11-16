package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// No CLI test may touch the network: every mode runs on the offline models.
func init() {
	resolveModels = func(context.Context, bool) Models { return offlineModels("test") }
}

func tempRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "strategy.md"), []byte(BaselineStrategy().Render()), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func runCLI(t *testing.T, in io.Reader, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(context.Background(), args, in, &out)
	return out.String(), err
}

func TestCLISelfImproveSequentialAndReplay(t *testing.T) {
	t.Parallel()
	root := tempRoot(t)
	sp := filepath.Join(root, "strategy.md")
	out, err := runCLI(t, nil, "--self-improve", "--experiments=7", "--strategy="+sp)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NOT an LLM", "базова метрика", "keep", "refused", "generalization", "max_experiments_reached"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out, err = runCLI(t, nil, "--replay", "--strategy="+sp)
	if err != nil || !strings.Contains(out, "журнал цілісний: 7 записів") {
		t.Fatalf("replay: %v\n%s", err, out)
	}
	// A second run continues the numbering of the same file.
	if _, err := runCLI(t, nil, "--self-improve", "--experiments=1", "--strategy="+sp); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, nil, "--replay", "--strategy="+sp); err != nil || !strings.Contains(out, "8 записів") {
		t.Fatalf("second run: %v\n%s", err, out)
	}
}

func TestCLISelfImproveParallel(t *testing.T) {
	t.Parallel()
	sp := filepath.Join(tempRoot(t), "strategy.md")
	out, err := runCLI(t, nil, "--self-improve", "--parallel=3", "--experiments=6", "--strategy="+sp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "паралельно 3 кандидатів/раунд") || !strings.Contains(out, "keep") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestCLIEvaluate(t *testing.T) {
	t.Parallel()
	sp := filepath.Join(tempRoot(t), "strategy.md")
	out, err := runCLI(t, nil, "--evaluate", "--set=generalization", "--strategy="+sp)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| G1 |", "метрика:", "за типами фактів", "duration"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestCLIAsk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		in     io.Reader
		window string
		want   string
	}{
		{"approve", strings.NewReader("approve\n"), "1m", "затверджено: human"},
		{"garbage is not a decision", strings.NewReader("maybe\n"), "1m", "ЗВІТ НЕ ВИПУЩЕНО (invalid"},
		{"silence is a refusal", blockingReader{}, "50ms", "ЗВІТ НЕ ВИПУЩЕНО (timed_out, system:timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := tempRoot(t)
			out, err := runCLI(t, tt.in, "--ask=Who approves a refund above 2,500 EUR?",
				"--window="+tt.window, "--strategy="+filepath.Join(root, "strategy.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "ПАУЗА") || !strings.Contains(out, tt.want) {
				t.Fatalf("output:\n%s", out)
			}
			log, _ := os.ReadFile(filepath.Join(root, "approvals.log"))
			if strings.Count(string(log), "\n") != 2 {
				t.Fatalf("approvals.log must have the request and the decision:\n%s", log)
			}
		})
	}
}

// blockingReader never answers: the human is silent.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) { time.Sleep(time.Hour); return 0, io.EOF }

func TestCLIErrors(t *testing.T) {
	t.Parallel()
	root := tempRoot(t)
	sp := filepath.Join(root, "strategy.md")
	for _, args := range [][]string{
		{"--evaluate", "--strategy=" + filepath.Join(root, "other.md")},
		{"--self-improve", "--experiments=0", "--strategy=" + sp},
		{"--strategy=" + sp},
		{"--evaluate", "--eval=" + t.TempDir(), "--strategy=" + sp},
		{"--replay", "--strategy=" + sp},
		{"--bogus-flag"},
	} {
		if _, err := runCLI(t, nil, args...); err == nil {
			t.Errorf("run(%v) = nil error", args)
		}
	}
	if err := os.Remove(sp); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, nil, "--evaluate", "--strategy="+sp); err == nil {
		t.Error("missing strategy.md accepted")
	}
	if s := gitSHA(t.TempDir()); s != "unknown" {
		t.Errorf("gitSHA outside a repo = %q", s)
	}
}
