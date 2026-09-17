package indexing_test

import (
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/indexing"
)

func TestParseCompletenessIsNotInferredFromEmptySymbols(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		complete         bool
	}{
		{"empty valid package", "empty.go", "package p\n", true},
		{"valid symbol", "valid.go", "package p\nfunc Used() {}\n", true},
		{"unsupported", "file.txt", "ordinary text", false},
		{"oversized", "large.go", "package p\n" + strings.Repeat(" ", 2*1024*1024), false},
		{"syntax recovery", "broken.go", "package p\nfunc Broken( {\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := indexing.ParseFile(tc.path, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if file.Complete != tc.complete {
				t.Fatalf("Complete=%v, want %v", file.Complete, tc.complete)
			}
		})
	}
}
