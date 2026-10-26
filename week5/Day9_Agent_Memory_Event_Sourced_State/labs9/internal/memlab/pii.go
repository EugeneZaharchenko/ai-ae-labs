package memlab

import "regexp"

// PII patterns for the shapes that actually show up in merchant-support
// chats: e-mail, phone, payment card, IBAN. The list is deliberately short and
// deterministic — a regexp pass is a floor, not a DLP product. Anything that
// must never be stored (full card numbers, passports) needs a real detector in
// front of the model as well.
var piiPatterns = []struct {
	label string
	re    *regexp.Regexp
}{
	{"email", regexp.MustCompile(`[\p{L}0-9._%+\-]+@[\p{L}0-9.\-]+\.\p{L}{2,}`)},
	{"iban", regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`)},
	{"card", regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`)},
	{"phone", regexp.MustCompile(`\+?\d{2,3}[ -]?\(?\d{2,3}\)?[ -]?\d{3}[ -]?\d{2}[ -]?\d{2}\b`)},
}

// Redact replaces every PII match with «[redacted:<label>]» and reports how
// many matches it replaced.
func Redact(s string) (string, int) {
	n := 0
	for _, p := range piiPatterns {
		s = p.re.ReplaceAllStringFunc(s, func(string) string {
			n++
			return "[redacted:" + p.label + "]"
		})
	}
	return s, n
}

// ContainsPII reports whether Redact would change s.
func ContainsPII(s string) bool {
	_, n := Redact(s)
	return n > 0
}
