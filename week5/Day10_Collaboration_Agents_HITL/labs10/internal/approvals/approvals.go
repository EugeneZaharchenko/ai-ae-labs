// Package approvals is the lab's durable approval ledger: one SQLite file that
// holds the ADK sessions AND the audit record of every human decision.
//
// Why they share a file: a paused workflow is only half of a pending approval.
// The other half is "who was asked, about what exactly, until when, and what
// did they answer". Keeping both in one database means a process restart loses
// neither, and an auditor can join a decision to the session events that
// produced it.
//
// The ledger enforces four rules the graph alone cannot:
//
//   - every request gets a deadline (silence is a decision — a denial);
//   - a decision is written once (a second "approve" after a "deny" is an error);
//   - an approval authorises ONE artifact for ONE recipient list (the hash and
//     the recipients are re-checked at send time);
//   - an approval is consumed by the send, so a workflow node that re-runs after
//     a resume cannot send twice.
//
// Verified against google.golang.org/adk/v2 v2.6.0 (станом на 10/2026).
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/glebarez/sqlite"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Status is the life-cycle state of one approval request.
type Status string

const (
	// Pending — asked, nobody has answered yet.
	Pending Status = "pending"
	// Approved — a named human said yes.
	Approved Status = "approved"
	// Denied — a named human said no.
	Denied Status = "denied"
	// TimedOut — nobody answered inside the window. A denial with its own reason.
	TimedOut Status = "timed_out"
	// Invalid — a reply arrived that could not be read. Also a denial.
	Invalid Status = "invalid"
)

// Errors a caller can branch on with errors.Is.
var (
	// ErrNotFound — no request with that ID.
	ErrNotFound = errors.New("approvals: запит не знайдено")
	// ErrAlreadyDecided — the request already has a final status.
	ErrAlreadyDecided = errors.New("approvals: рішення вже ухвалено")
	// ErrNotApproved — the side effect asked to run without a matching approval.
	ErrNotApproved = errors.New("approvals: немає схвалення на цю дію")
	// ErrAlreadyUsed — the approval was already spent on a send.
	ErrAlreadyUsed = errors.New("approvals: схвалення вже використано")
)

// Request is what the human is asked to approve. Hash pins the artifact:
// approving v1 of a document does not approve v2.
type Request struct {
	InterruptID string
	SessionID   string
	Action      string
	ArtifactURI string
	Hash        string
	Recipients  []string
	// Facts is the evidence shown to the reviewer, kept verbatim for the audit.
	Facts map[string]any
	// Window is how long silence is tolerated before the request times out.
	Window time.Duration
}

// Decision is the final answer, whoever or whatever gave it.
type Decision struct {
	Status Status
	// By is a person ("oksana") or a system actor ("system:timeout").
	By     string
	Reason string
}

// Record is one row of the audit trail: who, what, when, decision.
type Record struct {
	InterruptID string `gorm:"primaryKey"`
	SessionID   string
	Action      string
	ArtifactURI string
	Hash        string
	Recipients  string // JSON array
	Facts       string // JSON object
	Status      Status `gorm:"index"`
	RequestedAt time.Time
	ExpiresAt   time.Time
	DecidedBy   string
	DecidedAt   *time.Time
	Reason      string
	ConsumedAt  *time.Time
}

// RecipientList decodes the stored recipient list.
func (r Record) RecipientList() []string {
	var out []string
	_ = json.Unmarshal([]byte(r.Recipients), &out)
	return out
}

// Store is the open SQLite file.
type Store struct {
	db       *gorm.DB
	sessions session.Service
	now      func() time.Time
}

// Open opens (or creates) the database at path and migrates every table.
// WAL lets a second process read the ledger while the first one is paused.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("approvals: %w", err)
		}
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("approvals: open %s: %w", path, err)
	}
	sessions, err := database.NewSessionServiceFromDB(db)
	if err != nil {
		return nil, fmt.Errorf("approvals: session service: %w", err)
	}
	if err := database.AutoMigrate(sessions); err != nil {
		return nil, fmt.Errorf("approvals: migrate sessions: %w", err)
	}
	if err := db.AutoMigrate(&Record{}); err != nil {
		return nil, fmt.Errorf("approvals: migrate ledger: %w", err)
	}
	return &Store{db: db, sessions: sessions, now: time.Now}, nil
}

// SetClock replaces the time source. Tests freeze it; production never calls it.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Sessions is the durable ADK session.Service backed by the same file.
func (s *Store) Sessions() session.Service { return s.sessions }

// Close releases the file.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("approvals: %w", err)
	}
	return sqlDB.Close()
}

// Ask records a pending request. Asking twice with the same InterruptID is a
// no-op: a node that re-runs after resume must not reset the deadline.
func (s *Store) Ask(ctx context.Context, r Request) (Record, error) {
	if r.InterruptID == "" || r.Hash == "" || len(r.Recipients) == 0 {
		return Record{}, errors.New("approvals: запит без InterruptID, hash або отримувачів")
	}
	if r.Window <= 0 {
		r.Window = 2 * time.Minute
	}
	recips, _ := json.Marshal(r.Recipients)
	facts, _ := json.Marshal(r.Facts)
	now := s.now()
	rec := Record{
		InterruptID: r.InterruptID, SessionID: r.SessionID, Action: r.Action,
		ArtifactURI: r.ArtifactURI, Hash: r.Hash,
		Recipients: string(recips), Facts: string(facts),
		Status: Pending, RequestedAt: now, ExpiresAt: now.Add(r.Window),
	}
	res := s.db.WithContext(ctx).Where("interrupt_id = ?", r.InterruptID).FirstOrCreate(&rec)
	if res.Error != nil {
		return Record{}, fmt.Errorf("approvals: ask %s: %w", r.InterruptID, res.Error)
	}
	return rec, nil
}

// Get returns one record.
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	var rec Record
	err := s.db.WithContext(ctx).First(&rec, "interrupt_id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Record{}, fmt.Errorf("approvals: get %s: %w", id, err)
	}
	return rec, nil
}

// Decide writes the final answer exactly once. The UPDATE is conditional on
// status = pending, so two reviewers racing, or a late "approve" arriving after
// the timeout sweep, cannot overwrite a decision already made.
func (s *Store) Decide(ctx context.Context, id string, d Decision) (Record, error) {
	if d.Status == Pending || d.Status == "" {
		return Record{}, errors.New("approvals: рішення не може бути pending")
	}
	now := s.now()
	res := s.db.WithContext(ctx).Model(&Record{}).
		Where("interrupt_id = ? AND status = ?", id, Pending).
		Updates(map[string]any{"status": d.Status, "decided_by": d.By, "reason": d.Reason, "decided_at": now})
	if res.Error != nil {
		return Record{}, fmt.Errorf("approvals: decide %s: %w", id, res.Error)
	}
	rec, err := s.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if res.RowsAffected == 0 {
		return rec, fmt.Errorf("%w: %s вже %s (by %s)", ErrAlreadyDecided, id, rec.Status, rec.DecidedBy)
	}
	return rec, nil
}

// PendingList returns every request still waiting, oldest first.
func (s *Store) PendingList(ctx context.Context) ([]Record, error) {
	var out []Record
	err := s.db.WithContext(ctx).Where("status = ?", Pending).Order("requested_at").Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("approvals: pending: %w", err)
	}
	return out, nil
}

// Expire turns every overdue pending request into TimedOut and returns them.
// The caller resumes each paused workflow with a denial, so the run ends with a
// named reason instead of hanging.
func (s *Store) Expire(ctx context.Context) ([]Record, error) {
	pending, err := s.PendingList(ctx)
	if err != nil {
		return nil, err
	}
	var out []Record
	now := s.now()
	for _, p := range pending {
		if p.ExpiresAt.After(now) {
			continue
		}
		reason := fmt.Sprintf("ніхто не відповів до %s", p.ExpiresAt.UTC().Format(time.RFC3339))
		rec, err := s.Decide(ctx, p.InterruptID, Decision{Status: TimedOut, By: "system:timeout", Reason: reason})
		if errors.Is(err, ErrAlreadyDecided) {
			continue // a human answered between the SELECT and the UPDATE
		}
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Spend checks that an approval covers exactly this artifact and these
// recipients and marks it used, in one conditional UPDATE. It is the last line
// of defence: whatever the graph wiring does, the send cannot happen without a
// matching, unspent, human approval.
func (s *Store) Spend(ctx context.Context, id, hash string, recipients []string) error {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotApproved, err)
	}
	switch {
	case rec.Status != Approved:
		return fmt.Errorf("%w: статус %q", ErrNotApproved, rec.Status)
	case rec.Hash != hash:
		return fmt.Errorf("%w: документ змінився після схвалення", ErrNotApproved)
	case !sameSet(rec.RecipientList(), recipients):
		return fmt.Errorf("%w: список отримувачів не збігається зі схваленим", ErrNotApproved)
	}
	res := s.db.WithContext(ctx).Model(&Record{}).
		Where("interrupt_id = ? AND status = ? AND consumed_at IS NULL", id, Approved).
		Update("consumed_at", s.now())
	if res.Error != nil {
		return fmt.Errorf("approvals: spend %s: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", ErrAlreadyUsed, id)
	}
	return nil
}

// Unspend returns a spent approval after a failed send, so the retry is allowed
// but the audit keeps the reason. At-most-once is the default; this is the
// explicit, logged exception.
func (s *Store) Unspend(ctx context.Context, id, why string) error {
	err := s.db.WithContext(ctx).Model(&Record{}).
		Where("interrupt_id = ?", id).
		Updates(map[string]any{"consumed_at": nil, "reason": why}).Error
	if err != nil {
		return fmt.Errorf("approvals: unspend %s: %w", id, err)
	}
	return nil
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
