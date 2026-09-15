// Package redact removes credential values from text before it is stored or
// shown. It has no dependencies so both the runner and ci-runner can use it:
// the runner redacts before a line leaves the job's host, ci-runner redacts
// again before a line is stored, so a runner that does not (an old one, or a
// compromised one) still cannot put a brokered value into a job's log.
package redact

import (
	"sort"
	"strings"
)

// Mask replaces a redacted value.
const Mask = "***"

// minLength is the shortest value redacted. Masking a one- or two-character
// value would mangle every log line that happens to contain those characters
// while protecting nothing a guess would not find.
const minLength = 4

// Redactor replaces a fixed set of values.
type Redactor struct {
	values []string
}

// New builds a Redactor for values. Longer values are replaced first, so a
// value containing another is masked whole rather than leaving its remainder.
func New(values []string) *Redactor {
	var keep []string
	for _, v := range values {
		if len(v) >= minLength {
			keep = append(keep, v)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return len(keep[i]) > len(keep[j]) })
	return &Redactor{values: keep}
}

// Line returns s with every value masked.
func (r *Redactor) Line(s string) string {
	if r == nil {
		return s
	}
	for _, v := range r.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Mask)
		}
	}
	return s
}

// Values returns the values of a map, for building a Redactor from secret env.
func Values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
