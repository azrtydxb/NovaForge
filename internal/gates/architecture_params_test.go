package gates_test

import (
	"context"
	"testing"

	"github.com/novaforge/novaforge/internal/gates"
)

func TestArchitectureRejectsMalformedConstraints(t *testing.T) {
	for name, value := range map[string]any{
		"null":        nil,
		"scalar":      "frontend -> database",
		"mapping":     map[string]any{"frontend": "database"},
		"mixed-list":  []any{"database -> frontend", 12},
		"empty-from":  []any{" -> database"},
		"empty-to":    []string{"frontend -> "},
		"extra-arrow": []string{"frontend -> database -> another"},
	} {
		t.Run(name, func(t *testing.T) {
			eval, err := gates.Runners["architecture"](context.Background(), gates.Input{WorkDir: archFixture(t), Params: map[string]any{"forbidden_dependencies": value}})
			if err != nil {
				t.Fatal(err)
			}
			if eval.Status != "error" {
				t.Fatalf("malformed constraint silently weakened policy: status=%s detail=%s", eval.Status, eval.Detail)
			}
		})
	}
}
