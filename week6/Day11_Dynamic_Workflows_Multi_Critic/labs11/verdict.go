package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Critic roles. "compiler" is the objective critic (go build/vet/test),
// the other three are LLM critics with different prompts.
const (
	RoleCompiler    = "compiler"
	RoleCorrectness = "correctness"
	RoleStyle       = "style"
	RoleSecurity    = "security"
)

// llmRoles is the LLM part of the panel, in a fixed order for logs.
var llmRoles = []string{RoleCorrectness, RoleStyle, RoleSecurity}

func knownRole(r string) bool {
	switch r {
	case RoleCompiler, RoleCorrectness, RoleStyle, RoleSecurity:
		return true
	}
	return false
}

// ErrBadVerdict marks a critic answer that does not match the Verdict schema.
var ErrBadVerdict = errors.New("невалідний вердикт")

// verdictJSON is only for decoding: with pointers I can tell
// "field is missing" from "field is false/empty".
type verdictJSON struct {
	Critic   *string  `json:"critic"`
	Passed   *bool    `json:"passed"`
	Comments []string `json:"comments"`
}

// ParseVerdict turns a raw critic answer into a Verdict or an error.
// A fence or some text around the JSON is ok (models do it all the time),
// but the JSON itself must be valid and must pass ValidateVerdict.
func ParseVerdict(raw, role string) (Verdict, error) {
	obj, ok := firstJSONObject(stripFence(raw))
	if !ok {
		return Verdict{}, fmt.Errorf("%w: у відповіді критика %q немає JSON: %.80q", ErrBadVerdict, role, raw)
	}
	var vj verdictJSON
	if err := json.Unmarshal(obj, &vj); err != nil {
		return Verdict{}, fmt.Errorf("%w: JSON не підходить під схему (%v)", ErrBadVerdict, err)
	}
	if vj.Critic == nil || vj.Passed == nil {
		return Verdict{}, fmt.Errorf("%w: немає обов'язкового поля critic або passed", ErrBadVerdict)
	}
	v := Verdict{Critic: *vj.Critic, Passed: *vj.Passed, Comments: cleanComments(vj.Comments)}
	if err := ValidateVerdict(v, role); err != nil {
		return Verdict{}, err
	}
	return v, nil
}

// ValidateVerdict checks the verdict against the role we asked.
//
// Note: Homework.md also says "passed=true with comments is invalid", but the
// starter test (main_test.go) expects {"passed":true,"comments":["ок"]} to be
// accepted. I followed the test: such comments are allowed, but they are NOT
// sent to the maker as feedback (see decide).
func ValidateVerdict(v Verdict, role string) error {
	if !knownRole(v.Critic) {
		return fmt.Errorf("%w: невідома роль %q", ErrBadVerdict, v.Critic)
	}
	if role != "" && v.Critic != role {
		return fmt.Errorf("%w: чекали роль %q, а критик назвався %q", ErrBadVerdict, role, v.Critic)
	}
	if !v.Passed && len(v.Comments) == 0 {
		return fmt.Errorf("%w: %q поставив passed=false без жодного коментаря", ErrBadVerdict, v.Critic)
	}
	return nil
}

// rejectedVerdict is what I put instead of a broken answer: it counts as
// passed=false with a system comment, so a broken critic can never "approve".
func rejectedVerdict(role string, err error) Verdict {
	return Verdict{
		Critic:   role,
		Passed:   false,
		Comments: []string{"[system] відповідь критика відхилено: " + err.Error()},
	}
}

// decide accepts the code only if every critic passed. Comments of the
// critics that failed become feedback for the next maker attempt.
func decide(verdicts []Verdict) (bool, []string) {
	accept := len(verdicts) > 0
	var feedback []string
	for _, v := range verdicts {
		if v.Passed {
			continue
		}
		accept = false
		for _, c := range v.Comments {
			feedback = append(feedback, "["+v.Critic+"] "+c)
		}
	}
	return accept, feedback
}

// firstJSONObject decodes exactly one JSON object starting at the first '{'.
// Anything after it is ignored.
func firstJSONObject(s string) (json.RawMessage, bool) {
	i := strings.IndexByte(s, '{')
	if i < 0 {
		return nil, false
	}
	var raw json.RawMessage
	if err := json.NewDecoder(strings.NewReader(s[i:])).Decode(&raw); err != nil {
		return nil, false
	}
	return raw, true
}

func cleanComments(in []string) []string {
	var out []string
	for _, c := range in {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}
