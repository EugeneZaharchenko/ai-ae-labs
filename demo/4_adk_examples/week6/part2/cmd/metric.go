package main

import "strings"

// The single fitness metric: fact coverage.
//
// Each eval question has golden facts; an answer scores the share of them it
// contains. The metric is a PURE FUNCTION OF THE ANSWER TEXT. The model answers,
// but only deterministic code decides what the answer is worth — there is no
// judge to flatter, which is why it is hard to talk the metric into a better
// number. (The cost of that choice: it measures whether the figure is present,
// not whether the sentence around it is sensible. A real eval would add checks;
// a single unambiguous number is the autoresearch discipline, and the price of
// it is named here rather than discovered later.)
//
// Direction: HIGHER is better. karpathy/autoresearch minimizes validation
// bits-per-byte; the direction is a convention, the single number is the point.

// Fact is one thing a correct answer contains. Any of Forms counts; they are
// alternative spellings of one figure, not alternative facts.
type Fact struct {
	Name  string
	Forms []string
}

// Case is one eval question with its golden facts.
type Case struct {
	ID       string
	Question string
	Facts    []Fact
}

func fact(name string, forms ...string) Fact { return Fact{Name: name, Forms: forms} }

// Dev is the set the loop optimizes against: the keep/discard decision is made
// on it, and the proposer sees how each question did.
func Dev() []Case {
	return []Case{
		{"D1", "A merchant asks for a 7,500 EUR refund on a transaction from 100 days ago. What approval and timing apply?",
			[]Fact{fact("second approval", "compliance officer"), fact("processing time", "5 business days"), fact("window", "120 days")}},
		{"D2", "By when must merchant evidence for a chargeback be submitted, what fee applies, and which team handles reason code 4837?",
			[]Fact{fact("evidence deadline", "7 calendar days"), fact("fee", "15 eur"), fact("team", "fraud team")}},
		{"D3", "When does a standard payout arrive for a Growth-tariff merchant, what is the cut-off, and what does an instant payout cost?",
			[]Fact{fact("schedule", "t+2"), fact("cut-off", "14:00"), fact("instant fee", "1.0%", "1%")}},
	}
}

// Found returns how many of the case's facts the answer contains, and the names
// of the ones it does not.
func (c Case) Found(answer string) (found int, missed []string) {
	a := strings.ToLower(answer)
	for _, f := range c.Facts {
		hit := false
		for _, form := range f.Forms {
			if strings.Contains(a, form) {
				hit = true
				break
			}
		}
		if hit {
			found++
		} else {
			missed = append(missed, f.Name)
		}
	}
	return found, missed
}

// Measurement is one strategy's result over the dev set.
type Measurement struct {
	Found, Total int
	Answers      map[string]string // case ID → the worker's answer
	PerCase      map[string]float64
	Reads        int // kb_read calls served: did the strategy actually open documents?
}

// Score is the one number: facts found over facts asked, across all cases.
func (m Measurement) Score() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Found) / float64(m.Total)
}
