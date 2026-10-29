package approvals

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	clock := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return clock })
	return s, &clock
}

func req(id string) Request {
	return Request{
		InterruptID: id, SessionID: "s1", Action: "send_plan_to_board",
		ArtifactURI: "file:///plan.html", Hash: "h1",
		Recipients: []string{"oksana@ledgerworks.ua", "board@ledgerworks.ua"},
		Facts:      map[string]any{"risk": "medium"}, Window: time.Minute,
	}
}

func TestAskIsIdempotentAndKeepsTheFirstDeadline(t *testing.T) {
	s, clock := openTest(t)
	ctx := context.Background()
	first, err := s.Ask(ctx, req("a"))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	*clock = clock.Add(30 * time.Second)
	again, err := s.Ask(ctx, req("a"))
	if err != nil {
		t.Fatalf("Ask again: %v", err)
	}
	if !again.ExpiresAt.Equal(first.ExpiresAt) {
		t.Errorf("повторний Ask зсунув дедлайн: %v -> %v", first.ExpiresAt, again.ExpiresAt)
	}
	if got := len(first.RecipientList()); got != 2 {
		t.Errorf("RecipientList = %d, want 2", got)
	}
}

func TestAskRejectsIncompleteRequests(t *testing.T) {
	s, _ := openTest(t)
	for name, r := range map[string]Request{
		"no id":         {Hash: "h", Recipients: []string{"a@b.c"}},
		"no hash":       {InterruptID: "x", Recipients: []string{"a@b.c"}},
		"no recipients": {InterruptID: "x", Hash: "h"},
	} {
		if _, err := s.Ask(context.Background(), r); err == nil {
			t.Errorf("%s: очікувалась помилка", name)
		}
	}
}

func TestDefaultWindowApplies(t *testing.T) {
	s, _ := openTest(t)
	r := req("w")
	r.Window = 0
	rec, err := s.Ask(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.ExpiresAt.Sub(rec.RequestedAt); got != 2*time.Minute {
		t.Errorf("вікно за замовчуванням = %v, want 2m", got)
	}
}

func TestDecideWritesOnce(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	_, _ = s.Ask(ctx, req("a"))
	rec, err := s.Decide(ctx, "a", Decision{Status: Denied, By: "oksana", Reason: "немає розділу про НБУ"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if rec.Status != Denied || rec.DecidedBy != "oksana" || rec.DecidedAt == nil {
		t.Errorf("запис без who/when: %+v", rec)
	}
	// A late approve must not overwrite the denial.
	_, err = s.Decide(ctx, "a", Decision{Status: Approved, By: "mallory"})
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("err = %v, want ErrAlreadyDecided", err)
	}
	got, _ := s.Get(ctx, "a")
	if got.Status != Denied || got.DecidedBy != "oksana" {
		t.Errorf("рішення перезаписано: %+v", got)
	}
}

func TestDecideValidation(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	if _, err := s.Decide(ctx, "a", Decision{Status: Pending}); err == nil {
		t.Error("Decide(pending) має бути помилкою")
	}
	if _, err := s.Decide(ctx, "missing", Decision{Status: Approved}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get err = %v, want ErrNotFound", err)
	}
}

func TestExpireTurnsSilenceIntoNamedDenial(t *testing.T) {
	s, clock := openTest(t)
	ctx := context.Background()
	_, _ = s.Ask(ctx, req("slow"))
	_, _ = s.Ask(ctx, req("answered"))
	_, _ = s.Decide(ctx, "answered", Decision{Status: Approved, By: "oksana"})

	if got, _ := s.Expire(ctx); len(got) != 0 {
		t.Fatalf("до дедлайну нічого не має спливати: %+v", got)
	}
	*clock = clock.Add(61 * time.Second)
	expired, err := s.Expire(ctx)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if len(expired) != 1 || expired[0].InterruptID != "slow" {
		t.Fatalf("expired = %+v, want only slow", expired)
	}
	if expired[0].Status != TimedOut || expired[0].DecidedBy != "system:timeout" || expired[0].Reason == "" {
		t.Errorf("timeout без who/why: %+v", expired[0])
	}
	if pend, _ := s.PendingList(ctx); len(pend) != 0 {
		t.Errorf("pending після Expire = %+v", pend)
	}
}

func TestSpendRequiresMatchingUnspentApproval(t *testing.T) {
	ctx := context.Background()
	recips := []string{"board@ledgerworks.ua", "oksana@ledgerworks.ua"} // other order

	tests := []struct {
		name    string
		prepare func(*Store)
		hash    string
		to      []string
		want    error
	}{
		{"pending", func(*Store) {}, "h1", recips, ErrNotApproved},
		{"denied", func(s *Store) { _, _ = s.Decide(ctx, "a", Decision{Status: Denied, By: "o"}) }, "h1", recips, ErrNotApproved},
		{"timed out", func(s *Store) { _, _ = s.Decide(ctx, "a", Decision{Status: TimedOut, By: "system:timeout"}) }, "h1", recips, ErrNotApproved},
		{"unknown id", func(*Store) {}, "h1", recips, ErrNotApproved},
		{"document changed", func(s *Store) { _, _ = s.Decide(ctx, "a", Decision{Status: Approved, By: "o"}) }, "h2", recips, ErrNotApproved},
		{"recipients widened", func(s *Store) { _, _ = s.Decide(ctx, "a", Decision{Status: Approved, By: "o"}) }, "h1",
			append([]string{"press@example.com"}, recips...), ErrNotApproved},
		{"ok", func(s *Store) { _, _ = s.Decide(ctx, "a", Decision{Status: Approved, By: "o"}) }, "h1", recips, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := openTest(t)
			_, _ = s.Ask(ctx, req("a"))
			tt.prepare(s)
			id := "a"
			if tt.name == "unknown id" {
				id = "zzz"
			}
			err := s.Spend(ctx, id, tt.hash, tt.to)
			if !errors.Is(err, tt.want) {
				t.Errorf("Spend err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSpendIsOneShotAndUnspendReopens(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	_, _ = s.Ask(ctx, req("a"))
	_, _ = s.Decide(ctx, "a", Decision{Status: Approved, By: "oksana"})
	to := []string{"oksana@ledgerworks.ua", "board@ledgerworks.ua"}

	if err := s.Spend(ctx, "a", "h1", to); err != nil {
		t.Fatalf("перший Spend: %v", err)
	}
	if err := s.Spend(ctx, "a", "h1", to); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatalf("другий Spend err = %v, want ErrAlreadyUsed", err)
	}
	if err := s.Unspend(ctx, "a", "send failed: smtp 421"); err != nil {
		t.Fatalf("Unspend: %v", err)
	}
	if err := s.Spend(ctx, "a", "h1", to); err != nil {
		t.Errorf("Spend після Unspend: %v", err)
	}
}

func TestLedgerSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	ctx := context.Background()
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s1.Ask(ctx, req("a"))
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	pend, err := s2.PendingList(ctx)
	if err != nil || len(pend) != 1 || pend[0].InterruptID != "a" {
		t.Fatalf("після «рестарту» pending = %+v, err = %v", pend, err)
	}
	if s2.Sessions() == nil {
		t.Error("Sessions() = nil")
	}
}

func TestOpenFailsOnUnwritablePath(t *testing.T) {
	if _, err := Open("/proc/nope/board.db"); err == nil {
		t.Error("очікувалась помилка")
	}
}
