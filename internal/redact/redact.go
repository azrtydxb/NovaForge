// Package redact removes credential values from text before it is stored or
// shown. Both the runner and ci-runner use the same masking rules:
// the runner redacts before a line leaves the job's host, ci-runner redacts
// again before a line is stored, so a runner that does not (an old one, or a
// compromised one) still cannot put a brokered value into a job's log.
package redact

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mask replaces a redacted value.
const Mask = "***"

// minLength preserves the historical threshold for opaque scalar values.
// Structured credential constituents and multiline components are always
// masked, even if short: their enclosing credential explicitly marked them.
const minLength = 4

// Redactor replaces a fixed set of values.
type Redactor struct {
	values []string
}

// New builds a Redactor for values. Longer values are replaced first, so a
// value containing another is masked whole rather than leaving its remainder.
func New(values []string) *Redactor {
	seen := make(map[string]bool)
	add := func(v string, structured bool) {
		if v != "" && (structured || len(v) >= minLength) {
			seen[v] = true
		}
		// Log transports split multiline values into lines. Mask each nonempty
		// component too, including PEM bodies and YAML block scalars.
		if strings.ContainsAny(v, "\r\n") {
			for _, line := range strings.FieldsFunc(v, func(r rune) bool { return r == '\r' || r == '\n' }) {
				if line != "" {
					seen[line] = true
				}
			}
		}
	}
	// Token traversal preserves duplicate object fields as well as number
	// spelling; decoding into float64 or a map would discard that evidence.
	var jsonScalars func(*json.Decoder)
	jsonScalars = func(decoder *json.Decoder) {
		// json.Valid has already checked the entire document and nesting;
		// traversal reads scalar tokens rather than allocating a second tree.
		token, err := decoder.Token()
		if err != nil {
			return
		}
		switch token := token.(type) {
		case json.Delim:
			for decoder.More() {
				if token == '{' {
					// Keys are not credential values.
					_, _ = decoder.Token()
				}
				jsonScalars(decoder)
			}
			_, _ = decoder.Token() // Closing delimiter of the validated container.
		case string:
			add(token, true)
		case json.Number:
			add(token.String(), true)
		case bool:
			add(strconv.FormatBool(token), true)
		case nil:
			add("null", true)
		}
	}
	var yamlScalars func(*yaml.Node)
	yamlScalars = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 1; i < len(n.Content); i += 2 {
				yamlScalars(n.Content[i])
			}
		case yaml.SequenceNode, yaml.DocumentNode:
			for _, child := range n.Content {
				yamlScalars(child)
			}
		case yaml.ScalarNode:
			add(n.Value, true)
		}
		// Aliases reference scalars already visited in the document; do not
		// follow them (recursive aliases must never recurse indefinitely).
	}
	for _, v := range values {
		add(v, false)
		// Valid JSON can contain UTF-16 surrogate escapes that yaml.v3
		// rejects. An unrelated metadata field must not disable all masking.
		if json.Valid([]byte(v)) {
			trimmed := strings.TrimSpace(v)
			if trimmed[0] == '{' || trimmed[0] == '[' {
				decoder := json.NewDecoder(strings.NewReader(v))
				decoder.UseNumber()
				jsonScalars(decoder)
			}
			continue
		}
		var doc yaml.Node
		// Only structured documents expand: ordinary scalar tokens retain
		// the existing minimum-length behavior, including in the YAML fallback.
		if yaml.Unmarshal([]byte(v), &doc) == nil && len(doc.Content) == 1 {
			root := doc.Content[0]
			if root.Kind == yaml.MappingNode || root.Kind == yaml.SequenceNode {
				yamlScalars(root)
			}
		}
	}
	keep := make([]string, 0, len(seen))
	for v := range seen {
		keep = append(keep, v)
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
