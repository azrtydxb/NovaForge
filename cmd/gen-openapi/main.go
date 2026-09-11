// Command gen-openapi writes api/openapi.yaml from the edge route table.
// Run it via `make openapi` after adding a route.
package main

import (
	"fmt"
	"os"

	"github.com/novaforge/novaforge/internal/edge"
)

func main() {
	out := "api/openapi.yaml"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.WriteFile(out, []byte(edge.OpenAPIDocument()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", out)
}
