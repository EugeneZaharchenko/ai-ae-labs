// Package agentdb is the lab's durable SQLite file (.data/agent.db).
//
// One file holds three things that must survive a crash:
//
//   - ADK sessions (google.golang.org/adk/v2/session/database);
//   - opened refund cases — the effect log that makes a retried
//     open_refund_case a no-op instead of a second case;
//   - the LLM-call counter — the evidence for the kill -9 test. It lives
//     outside the worker process on purpose: a counter in memory dies with the
//     process it is supposed to judge.
//
// Durable execution (Temporal) keeps its own file; memory lives in Dgraph.
package agentdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
)

// DefaultPath is where the worker keeps the file unless AGENT_DB says otherwise.
const DefaultPath = ".data/agent.db"

// DB is the open file.
type DB struct {
	db       *gorm.DB
	sessions session.Service
}

type refundCase struct {
	ID            string `gorm:"primaryKey"`
	TransactionID string
	MerchantID    string
	CreatedAt     time.Time
}

type llmCall struct {
	ID    uint `gorm:"primaryKey"`
	Model string
	At    time.Time
}

type llmUsage struct {
	ID           uint `gorm:"primaryKey"`
	Model        string
	PromptTokens int32
	OutputTokens int32
	At           time.Time
}

// Open opens (or creates) the SQLite file at path and migrates every table.
// WAL lets the starter read the counter while the worker writes.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("agentdb: %w", err)
		}
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("agentdb: open %s: %w", path, err)
	}
	sessions, err := database.NewSessionServiceFromDB(db)
	if err != nil {
		return nil, fmt.Errorf("agentdb: session service: %w", err)
	}
	if err := database.AutoMigrate(sessions); err != nil {
		return nil, fmt.Errorf("agentdb: migrate sessions: %w", err)
	}
	if err := db.AutoMigrate(&refundCase{}, &llmCall{}, &llmUsage{}); err != nil {
		return nil, fmt.Errorf("agentdb: migrate: %w", err)
	}
	return &DB{db: db, sessions: sessions}, nil
}

// Close releases the file.
func (d *DB) Close() error {
	sqlDB, err := d.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Sessions is the durable ADK session service.
func (d *DB) Sessions() session.Service { return d.sessions }

// Cases is the durable effect log of refund cases.
func (d *DB) Cases() ledger.Cases { return cases{d.db} }

type cases struct{ db *gorm.DB }

// Open inserts the case unless it exists; the primary key makes this atomic.
func (c cases) Open(ctx context.Context, id string, in ledger.RefundCaseInput) (bool, error) {
	res := c.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&refundCase{
		ID: id, TransactionID: in.TransactionID, MerchantID: in.MerchantID, CreatedAt: time.Now().UTC(),
	})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 0, nil
}

// CountCases reports how many distinct refund cases exist.
func (d *DB) CountCases(ctx context.Context) (int64, error) {
	var n int64
	err := d.db.WithContext(ctx).Model(&refundCase{}).Count(&n).Error
	return n, err
}

// RecordLLMCall appends one row per real model call.
func (d *DB) RecordLLMCall(ctx context.Context, model string) error {
	return d.db.WithContext(ctx).Create(&llmCall{Model: model, At: time.Now().UTC()}).Error
}

// CountLLMCalls reports how many real model calls were made, ever.
func (d *DB) CountLLMCalls(ctx context.Context) (int64, error) {
	var n int64
	err := d.db.WithContext(ctx).Model(&llmCall{}).Count(&n).Error
	return n, err
}

// RecordLLMUsage appends the token usage a provider reported for one call.
func (d *DB) RecordLLMUsage(ctx context.Context, model string, promptTokens, outputTokens int32) error {
	return d.db.WithContext(ctx).Create(&llmUsage{Model: model, PromptTokens: promptTokens, OutputTokens: outputTokens, At: time.Now().UTC()}).Error
}

// SumLLMTokens totals the prompt and output tokens of every recorded call.
func (d *DB) SumLLMTokens(ctx context.Context) (prompt, output int64, err error) {
	var sums struct{ P, O int64 }
	err = d.db.WithContext(ctx).Model(&llmUsage{}).
		Select("COALESCE(SUM(prompt_tokens), 0) AS p, COALESCE(SUM(output_tokens), 0) AS o").Scan(&sums).Error
	return sums.P, sums.O, err
}
