package gateconfig_test

import (
	"math"
	"testing"

	"github.com/novaforge/novaforge/internal/gateconfig"
)

func TestParameterContract(t *testing.T) {
	for _, tc := range []struct {
		name, gate, key string
		value           any
		valid           bool
	}{
		{"zero coverage", "tests", "minimum_coverage", 0, true},
		{"fractional coverage", "tests", "minimum_coverage", 80.5, true},
		{"full coverage", "tests", "minimum_coverage", int64(100), true},
		{"negative coverage", "tests", "minimum_coverage", -1, false},
		{"excess coverage", "tests", "minimum_coverage", 101, false},
		{"NaN", "tests", "minimum_coverage", math.NaN(), false},
		{"infinity", "tests", "minimum_coverage", math.Inf(1), false},
		{"string number", "tests", "minimum_coverage", "80", false},
		{"null coverage", "tests", "minimum_coverage", nil, false},
		{"integer docs", "documentation", "max_undocumented", 2.0, true},
		{"fraction docs", "documentation", "max_undocumented", 1.5, false},
		{"overflow docs", "documentation", "max_undocumented", float64(math.MaxInt64), false},
		{"empty rules", "architecture", "forbidden_dependencies", []any{}, true},
		{"typed rules", "architecture", "forbidden_dependencies", []string{"ui -> db"}, true},
		{"YAML rules", "architecture", "forbidden_dependencies", []any{"ui -> db"}, true},
		{"mixed rules", "architecture", "forbidden_dependencies", []any{"ui -> db", 1}, false},
		{"null rules", "architecture", "forbidden_dependencies", nil, false},
		{"empty rule side", "architecture", "forbidden_dependencies", []any{" -> db"}, false},
		{"extra arrow", "architecture", "forbidden_dependencies", []any{"ui -> db -> x"}, false},
		{"unknown key", "architecture", "forbidden_dependences", []any{"ui -> db"}, false},
		{"wrong gate", "quality", "minimum_coverage", 80, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := gateconfig.ValidateParameters(tc.gate, map[string]any{tc.key: tc.value})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}
