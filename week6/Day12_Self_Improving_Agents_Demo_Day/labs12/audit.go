package main

// Brake (e): the full audit trail, experiments.log — one JSON line per
// experiment, keep AND discard (compliance wants to see why we did not
// improve as much as how we did).
//
// Append-only by construction: AuditLog takes an io.Writer and has no Seek or
// Truncate. And the log lies outside the agent's surface of change:
// sandbox.IsMutable("experiments.log") is false. An agent that could edit its
// own history would have a narrative, not an audit.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Verdict of one experiment.
type ExpVerdict string

const (
	VerdictKeep           ExpVerdict = "keep"
	VerdictDiscard        ExpVerdict = "discard"
	VerdictRefused        ExpVerdict = "refused" // a brake stopped it before scoring
	VerdictProposerFailed ExpVerdict = "proposer_failed"
)

// Entry is one line of experiments.log.
type Entry struct {
	ID             int        `json:"id"`
	TS             time.Time  `json:"ts"`
	Attempt        int        `json:"attempt"`
	Worker         int        `json:"worker,omitempty"` // parallel mode: candidate slot
	Verdict        ExpVerdict `json:"verdict"`
	Reason         string     `json:"reason"`
	TargetPath     string     `json:"target_path"`
	DiffSummary    string     `json:"diff_summary"`
	PrevMetric     float64    `json:"prev_metric"`
	NewMetric      float64    `json:"new_metric"`
	Scored         bool       `json:"scored"`
	OldStrategy    string     `json:"old_strategy"`
	NewStrategy    string     `json:"new_strategy"`
	OldStrategySHA string     `json:"old_strategy_sha"`
	NewStrategySHA string     `json:"new_strategy_sha"`
	DiscardPath    string     `json:"discard_path,omitempty"`
	GitSHA         string     `json:"git_sha"`
	DurationSec    float64    `json:"duration_sec"`
	EvalTokens     int        `json:"eval_tokens"`
	ProposerTokens int        `json:"proposer_tokens"`
	Detail         string     `json:"detail,omitempty"`
}

// AuditLog is the append-only sink.
type AuditLog struct {
	mu   sync.Mutex
	w    io.Writer
	next int
}

// NewAuditLog continues a log whose last id is lastID: the integrity
// invariant belongs to the FILE, not to one process.
func NewAuditLog(w io.Writer, lastID int) *AuditLog {
	return &AuditLog{w: w, next: max(lastID, 0) + 1}
}

// Append writes e with the next id and returns the stored entry.
func (l *AuditLog) Append(e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.ID = l.next
	data, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("audit: %w", err)
	}
	if _, err := l.w.Write(append(data, '\n')); err != nil {
		return Entry{}, fmt.Errorf("audit: %w", err)
	}
	l.next++
	return e, nil
}

// ReadEntries parses a JSON-lines log.
func ReadEntries(r io.Reader) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("audit: рядок %d: %w", len(out)+1, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	return out, nil
}

// ReadLogFile reads a log file; a missing file is an empty log.
func ReadLogFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	defer f.Close()
	return ReadEntries(f)
}

// ErrAuditBroken reports a log that does not replay as one sequential history.
var ErrAuditBroken = errors.New("аудиторський слід порушено")

// Replayed is the state rebuilt from the log.
type Replayed struct {
	Baseline, Final                    float64
	Keeps, Discards, Refused, Failures int
}

// Replay rebuilds the run from the log and checks it: ids 1, 2, 3… without
// gaps, and each entry starts from the metric of the last keep. A deleted or
// reordered line is visible. Runs are separated by a change of baseline only
// when the previous run ended: a new run starts a new chain.
func Replay(entries []Entry) (Replayed, error) {
	if len(entries) == 0 {
		return Replayed{}, fmt.Errorf("%w: порожній лог", ErrAuditBroken)
	}
	st := Replayed{Baseline: entries[0].PrevMetric, Final: entries[0].PrevMetric}
	for i, e := range entries {
		if e.ID != i+1 {
			return Replayed{}, fmt.Errorf("%w: запис #%d має id=%d — рядок видалено або переставлено", ErrAuditBroken, i+1, e.ID)
		}
		if e.Attempt == 1 && e.Worker <= 1 {
			st.Final = e.PrevMetric // a new run measures its own baseline
		}
		if e.PrevMetric != st.Final {
			return Replayed{}, fmt.Errorf("%w: запис id=%d стартує з %.4f, а стан %.4f", ErrAuditBroken, e.ID, e.PrevMetric, st.Final)
		}
		switch e.Verdict {
		case VerdictKeep:
			st.Keeps++
			st.Final = e.NewMetric
		case VerdictDiscard:
			st.Discards++
		case VerdictRefused:
			st.Refused++
		case VerdictProposerFailed:
			st.Failures++
		default:
			return Replayed{}, fmt.Errorf("%w: id=%d має невідомий вердикт %q", ErrAuditBroken, e.ID, e.Verdict)
		}
	}
	return st, nil
}

func contentSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
