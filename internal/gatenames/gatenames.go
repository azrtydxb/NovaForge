// Package gatenames is the fixed set of gate names the platform enforces. It
// is its own package so the work service can refuse a Work Item requiring a
// gate that does not exist without importing the gate controller and the
// analysis tooling behind it.
package gatenames

import "sort"

var known = map[string]bool{
	"tests":             true,
	"architecture":      true,
	"security":          true,
	"api-compatibility": true,
	"dependencies":      true,
	"quality":           true,
	"documentation":     true,
}

// Known reports whether name is a gate the platform enforces.
func Known(name string) bool { return known[name] }

// All returns every gate name, sorted.
func All() []string {
	out := make([]string, 0, len(known))
	for n := range known {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
