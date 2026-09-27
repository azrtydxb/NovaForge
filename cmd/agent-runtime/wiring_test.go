package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Library transport tests cannot prove that the executable supplies the policy
// and cleanup callbacks. Pin the production seam as well as exercising it on kw.
func TestProductionRunnerWiresPricingAndMCP(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Runner" {
			return true
		}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok {
					fields[key.Name] = true
				}
			}
		}
		return true
	})
	for _, key := range []string{"PriceForModel", "MCPOptions"} {
		if !fields[key] {
			t.Errorf("production Runner does not supply %s", key)
		}
	}
}
