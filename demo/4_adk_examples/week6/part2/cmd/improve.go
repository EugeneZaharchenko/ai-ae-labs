package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// The improvement loop: the autoresearch pattern, with its five properties each
// in one visible place.
//
//	1. a tightly scoped mutable surface   — Surface: one file, strategy.md
//	2. a fixed, cheap evaluation budget    — Config.MaxExperiments, and a model-call cap per question in engine.go
//	3. a single, unambiguous fitness metric — Measurement.Score, a pure function of answer text
//	4. an auditable strategy artifact      — strategy.md itself, human-readable, kept only when it wins
//	5. a full audit trail                  — Record, appended to experiments.log for EVERY experiment
//
// The loop controller is plain Go and not an LLM, and not an ADK node, on
// purpose: the brakes must not depend on the thing being braked. An agent that
// could decide to run one more experiment, or to widen its own surface, has not
// been given a budget — it has been given a suggestion.

// Env is what the loop needs from the outside world. In production these are
// ADK agents; in a test they are plain functions.
type Env struct {
	// Measure runs the worker under a strategy over the dev set.
	Measure func(ctx context.Context, s Strategy) (Measurement, error)
	// Propose asks for a new strategy, given the best one so far and how it did.
	// It returns the raw answer; parsing and checking are the loop's job.
	Propose func(ctx context.Context, best Strategy, last Measurement) (string, error)
}

// Config bounds the run.
type Config struct {
	MaxExperiments int
	// MinGain is how much better a candidate must score than the best so far to
	// be kept. Without it, a model that rewords the same strategy keeps winning
	// on noise and the "improvement" is a random walk.
	MinGain float64
	Surface *Surface
	LogPath string
}

// Record is one line of the audit trail.
type Record struct {
	N       int       `json:"n"`
	At      time.Time `json:"at"`
	Summary string    `json:"summary"`
	Path    string    `json:"path"`
	Score   float64   `json:"score"`
	Best    float64   `json:"best"`
	Kept    bool      `json:"kept"`
	Reason  string    `json:"reason"`
}

// Result is what the loop produced.
type Result struct {
	Baseline Measurement
	Best     Strategy
	BestM    Measurement
	Records  []Record
}

// Improve runs the loop. Every experiment is logged, kept or not: a log of only
// the wins is a highlight reel, not an audit.
func Improve(ctx context.Context, cfg Config, env Env) (Result, error) {
	var res Result
	best := Baseline()
	if err := cfg.Surface.Write(DefaultPath, best.Render()); err != nil {
		return res, fmt.Errorf("write baseline: %w", err)
	}
	m, err := env.Measure(ctx, best)
	if err != nil {
		return res, fmt.Errorf("measure baseline: %w", err)
	}
	res.Baseline, res.BestM, res.Best = m, m, best

	for n := 1; n <= cfg.MaxExperiments; n++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		raw, err := env.Propose(ctx, res.Best, res.BestM)
		if err != nil {
			return res, fmt.Errorf("experiment %d: propose: %w", n, err)
		}
		rec := Record{N: n, At: time.Now().UTC(), Best: res.BestM.Score()}

		path, cand, err := ParseProposal(raw)
		rec.Path = path
		if err == nil {
			rec.Summary = cand.Title
			err = cand.Validate()
		}
		if err == nil {
			err = cfg.Surface.Check(path)
		}
		if err != nil {
			// A rejected proposal is an experiment that was run and discarded: it
			// cost a model call and it is in the log with its cause.
			rec.Reason = "rejected: " + err.Error()
			if errors.Is(err, ErrOutsideSurface) {
				rec.Reason = "rejected (surface): " + err.Error()
			}
			res.Records = append(res.Records, rec)
			if err := appendLog(cfg.LogPath, rec); err != nil {
				return res, err
			}
			continue
		}

		cm, err := env.Measure(ctx, cand)
		if err != nil {
			return res, fmt.Errorf("experiment %d: measure: %w", n, err)
		}
		rec.Score = cm.Score()
		switch {
		case rec.Score >= rec.Best+cfg.MinGain && rec.Score > rec.Best:
			if err := cfg.Surface.Write(path, cand.Render()); err != nil {
				return res, fmt.Errorf("experiment %d: keep: %w", n, err)
			}
			rec.Kept, rec.Reason = true, fmt.Sprintf("kept: %.2f → %.2f", rec.Best, rec.Score)
			res.Best, res.BestM = cand, cm
		case rec.Score < rec.Best:
			rec.Reason = fmt.Sprintf("discarded: regression (%.2f < best %.2f)", rec.Score, rec.Best)
		default:
			rec.Reason = fmt.Sprintf("discarded: no gain (%.2f vs best %.2f, minimum gain %.2f)", rec.Score, rec.Best, cfg.MinGain)
		}
		res.Records = append(res.Records, rec)
		if err := appendLog(cfg.LogPath, rec); err != nil {
			return res, err
		}
	}
	return res, nil
}

// appendLog adds one JSON line. O_APPEND, never truncate: the log is the one file
// in the directory that only ever grows.
func appendLog(path string, r Record) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	defer f.Close()
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	return nil
}
