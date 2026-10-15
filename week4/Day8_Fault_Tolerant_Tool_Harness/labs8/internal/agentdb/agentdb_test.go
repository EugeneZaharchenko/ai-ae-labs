package agentdb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
)

func TestDurableAcrossReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "agent.db")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	in := ledger.RefundCaseInput{TransactionID: "txn-2026-07-118845", MerchantID: "A-114"}
	if replayed, err := db.Cases().Open(ctx, "rc-1", in); err != nil || replayed {
		t.Fatalf("first open = %v, %v", replayed, err)
	}
	if replayed, err := db.Cases().Open(ctx, "rc-1", in); err != nil || !replayed {
		t.Fatalf("second open = %v, %v; want replayed", replayed, err)
	}
	for range 3 {
		if err := db.RecordLLMCall(ctx, "m"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Sessions().Create(ctx, &session.CreateRequest{AppName: "ledgerworks", UserID: "taras", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path) // a new process would do exactly this
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if n, err := again.CountCases(ctx); err != nil || n != 1 {
		t.Errorf("cases after reopen = %d, %v", n, err)
	}
	if n, err := again.CountLLMCalls(ctx); err != nil || n != 3 {
		t.Errorf("llm calls after reopen = %d, %v", n, err)
	}
	if _, err := again.Sessions().Get(ctx, &session.GetRequest{AppName: "ledgerworks", UserID: "taras", SessionID: "s1"}); err != nil {
		t.Errorf("session after reopen: %v", err)
	}
}

func TestOpenFailsOnUnwritableDir(t *testing.T) {
	t.Parallel()
	if _, err := Open("/dev/null/agent.db"); err == nil {
		t.Fatal("want an error for a path under a file")
	}
}

func TestClosedDBErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := db.Cases().Open(ctx, "x", ledger.RefundCaseInput{}); err == nil {
		t.Error("Cases.Open on a closed DB must fail")
	}
	if err := db.RecordLLMCall(ctx, "m"); err == nil {
		t.Error("RecordLLMCall on a closed DB must fail")
	}
	if _, err := db.CountLLMCalls(ctx); err == nil {
		t.Error("CountLLMCalls on a closed DB must fail")
	}
	if _, err := db.CountCases(ctx); err == nil {
		t.Error("CountCases on a closed DB must fail")
	}
	if err := db.RecordLLMUsage(ctx, "m", 1, 1); err == nil {
		t.Error("RecordLLMUsage on a closed DB must fail")
	}
	if _, _, err := db.SumLLMTokens(ctx); err == nil {
		t.Error("SumLLMTokens on a closed DB must fail")
	}
}

func TestTokenUsageSums(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if p, o, err := db.SumLLMTokens(ctx); err != nil || p != 0 || o != 0 {
		t.Fatalf("empty db: %d/%d, %v", p, o, err)
	}
	for _, u := range [][2]int32{{100, 10}, {250, 40}} {
		if err := db.RecordLLMUsage(ctx, "m", u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	if p, o, err := db.SumLLMTokens(ctx); err != nil || p != 350 || o != 50 {
		t.Errorf("sum = %d/%d, %v; want 350/50", p, o, err)
	}
}

func TestOpenRejectsNonDatabaseFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agent.db")
	if err := os.WriteFile(path, []byte("this is not sqlite, it is a text file of sufficient length to have a header"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("want an error for a file that is not a SQLite database")
	}
}
