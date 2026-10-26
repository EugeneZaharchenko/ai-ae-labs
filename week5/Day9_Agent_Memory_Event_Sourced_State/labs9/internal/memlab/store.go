package memlab

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/glebarez/sqlite"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"google.golang.org/genai"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// DefaultPath is the lab's durable file. One SQLite file holds the sessions
// (ADK's own tables) and the long-term memory (ours).
const DefaultPath = ".data/memory.db"

// Store is the open file with both services on it.
type Store struct {
	db *gorm.DB
	// Sessions is google.golang.org/adk/v2/session/database — events, StateDelta
	// and app:/user: state survive a process restart.
	Sessions session.Service
	// Memory is a persistent memory.Service with an ingest policy. ADK ships
	// memory.InMemoryService (gone on restart, ingests every text event
	// verbatim) and a Vertex AI one; there is no SQL one, so this is the
	// smallest honest version of what a production service has to add.
	Memory *MemoryService
}

// Open opens (or creates) the SQLite file and migrates every table.
func Open(path string, ingest IngestPolicy) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("memlab: %w", err)
		}
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("memlab: open %s: %w", path, err)
	}
	// SQLite takes one writer at a time. With several pooled connections two
	// concurrent AppendEvent calls upgrade read transactions to write ones and
	// one fails at once with SQLITE_BUSY — busy_timeout does not help, because
	// SQLite refuses the upgrade instead of waiting. One connection serializes
	// writers in the pool instead (Postgres would not need this).
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	sessions, err := database.NewSessionServiceFromDB(db)
	if err != nil {
		return nil, fmt.Errorf("memlab: session service: %w", err)
	}
	if err := database.AutoMigrate(sessions); err != nil {
		return nil, fmt.Errorf("memlab: migrate sessions: %w", err)
	}
	if err := db.AutoMigrate(&memoryRow{}); err != nil {
		return nil, fmt.Errorf("memlab: migrate memory: %w", err)
	}
	return &Store{db: db, Sessions: sessions, Memory: &MemoryService{db: db, policy: ingest}}, nil
}

// Close releases the file.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// IngestPolicy decides what AddSessionToMemory may keep.
type IngestPolicy struct {
	// TTL is the retention window; 0 keeps entries until Forget.
	TTL time.Duration
	// Now is injectable so retention tests do not sleep.
	Now func() time.Time
}

func (p IngestPolicy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

type memoryRow struct {
	ID        string `gorm:"primaryKey"` // session/event — one row per event, so re-adding a session is a no-op
	AppName   string `gorm:"index:idx_scope"`
	UserID    string `gorm:"index:idx_scope"`
	SessionID string
	Author    string
	Text      string
	At        time.Time
	ExpiresAt *time.Time
}

// MemoryService is a memory.Service on SQLite.
//
// What it adds over memory.InMemoryService, each one a production incident
// somebody already had:
//
//   - it survives a restart;
//   - it stores redacted text only — an e-mail typed into a chat does not
//     become a retrievable "memory" six months later;
//   - rows expire (TTL) and can be deleted per user (Forget);
//   - search is hard-scoped to (app, user): an empty scope returns nothing
//     instead of everything.
type MemoryService struct {
	db     *gorm.DB
	policy IngestPolicy
}

var _ memory.Service = (*MemoryService)(nil)

// AddSessionToMemory ingests the text events of s. Calling it again for the
// same session is safe: rows are keyed by event.
func (m *MemoryService) AddSessionToMemory(ctx context.Context, s session.Session) error {
	var rows []memoryRow
	for e := range s.Events().All() {
		if e.LLMResponse.Content == nil || e.LLMResponse.Partial {
			continue
		}
		text := eventText(e.LLMResponse.Content)
		if text == "" {
			continue
		}
		text, _ = Redact(text)
		row := memoryRow{
			ID: s.ID() + "/" + e.ID, AppName: s.AppName(), UserID: s.UserID(), SessionID: s.ID(),
			Author: e.Author, Text: text, At: e.Timestamp,
		}
		if m.policy.TTL > 0 {
			exp := e.Timestamp.Add(m.policy.TTL)
			row.ExpiresAt = &exp
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	err := m.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	if err != nil {
		return fmt.Errorf("memlab: add session to memory: %w", err)
	}
	return nil
}

// maxResults mirrors memory.InMemoryService, so the two behave alike.
const maxResults = 10

// SearchMemory ranks entries by the number of distinct query words they
// contain, like memory.InMemoryService, minus expired rows.
func (m *MemoryService) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	res := &memory.SearchResponse{}
	if req.AppName == "" || req.UserID == "" {
		return res, nil
	}
	words := tokens(req.Query)
	if len(words) == 0 {
		return res, nil
	}
	var rows []memoryRow
	err := m.db.WithContext(ctx).
		Where("app_name = ? AND user_id = ? AND (expires_at IS NULL OR expires_at > ?)", req.AppName, req.UserID, m.policy.now()).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("memlab: search memory: %w", err)
	}
	type scored struct {
		n   int
		row memoryRow
	}
	var hits []scored
	for _, r := range rows {
		lower := strings.ToLower(r.Text)
		have := tokens(r.Text)
		n := 0
		for w := range words {
			// Non-ASCII words match as substrings: Ukrainian inflects, and a
			// whole-word match would miss «інтеграції» when asked about «інтеграція».
			if _, ok := have[w]; ok || (!isASCII(w) && strings.Contains(lower, stem(w))) {
				n++
			}
		}
		if n > 0 {
			hits = append(hits, scored{n, r})
		}
	}
	slices.SortStableFunc(hits, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.n, a.n), a.row.At.Compare(b.row.At))
	})
	for _, h := range hits[:min(len(hits), maxResults)] {
		role := genai.RoleModel
		if h.row.Author == "user" {
			role = genai.RoleUser
		}
		res.Memories = append(res.Memories, memory.Entry{
			ID: h.row.ID, Author: h.row.Author, Timestamp: h.row.At,
			Content: genai.NewContentFromText(h.row.Text, genai.Role(role)),
		})
	}
	return res, nil
}

// Purge deletes expired rows; run it from a cron/Job, not from a request.
func (m *MemoryService) Purge(ctx context.Context) (int64, error) {
	res := m.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at <= ?", m.policy.now()).Delete(&memoryRow{})
	return res.RowsAffected, res.Error
}

// Forget deletes everything remembered about one user (right to erasure).
func (m *MemoryService) Forget(ctx context.Context, app, user string) (int64, error) {
	if app == "" || user == "" {
		return 0, fmt.Errorf("memlab: forget needs app and user")
	}
	res := m.db.WithContext(ctx).Where("app_name = ? AND user_id = ?", app, user).Delete(&memoryRow{})
	return res.RowsAffected, res.Error
}

// Count reports the rows for one user, expired included — for tests and audits.
func (m *MemoryService) Count(ctx context.Context, app, user string) (int64, error) {
	var n int64
	err := m.db.WithContext(ctx).Model(&memoryRow{}).Where("app_name = ? AND user_id = ?", app, user).Count(&n).Error
	return n, err
}

func eventText(c *genai.Content) string {
	var parts []string
	for _, p := range c.Parts {
		if p != nil && !p.Thought && strings.TrimSpace(p.Text) != "" {
			parts = append(parts, strings.TrimSpace(p.Text))
		}
	}
	return strings.Join(parts, " ")
}

func tokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) > 2 {
			out[w] = struct{}{}
		}
	}
	return out
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// stem trims a Ukrainian word to a crude root so inflected forms match.
func stem(w string) string {
	r := []rune(w)
	if len(r) > 6 {
		return string(r[:len(r)-2])
	}
	return w
}
