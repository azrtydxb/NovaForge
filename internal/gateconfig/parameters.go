// Package gateconfig defines the parameter contract shared by repository
// configuration loading and target-pinned gate resolution.
package gateconfig

import (
	"fmt"
	"math"
	"strings"
)

// ValidateParameters rejects unknown keys and values the runners would otherwise
// coerce or ignore. Adding a parameter requires implementing its consumer first.
func ValidateParameters(gate string, params map[string]any) error {
	for key, value := range params {
		switch {
		case gate == "tests" && key == "minimum_coverage":
			n, ok := number(value)
			if !ok || n < 0 || n > 100 {
				return fmt.Errorf("minimum_coverage must be a finite number between 0 and 100")
			}
		case gate == "documentation" && key == "max_undocumented":
			n, ok := number(value)
			// The runner converts through float64 to int. Reject its overflow
			// boundary as well as fractions, rather than silently changing policy.
			if !ok || n < 0 || math.Trunc(n) != n || n >= float64(int(^uint(0)>>1)) {
				return fmt.Errorf("max_undocumented must be a nonnegative representable integer")
			}
		case gate == "architecture" && key == "forbidden_dependencies":
			var rules []string
			switch list := value.(type) {
			case []string:
				rules = list
			case []any:
				for _, item := range list {
					rule, ok := item.(string)
					if !ok {
						return fmt.Errorf("forbidden_dependencies must contain only strings")
					}
					rules = append(rules, rule)
				}
			default:
				return fmt.Errorf("forbidden_dependencies must be a list of strings")
			}
			for _, rule := range rules {
				parts := strings.Split(rule, "->")
				if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
					return fmt.Errorf("forbidden_dependencies requires '<from> -> <to>' rules")
				}
			}
		default:
			return fmt.Errorf("unknown parameter %q for gate %q", key, gate)
		}
	}
	return nil
}

func number(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case float64:
		n = v
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}
