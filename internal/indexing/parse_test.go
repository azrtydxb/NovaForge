package indexing_test

import (
	"testing"

	"github.com/novaforge/novaforge/internal/indexing"
)

func TestParseGoFunctions(t *testing.T) {
	src := []byte("package p\n\nfunc Add(a, b int) int { return a + b }\n")
	symbols, _, err := indexing.Parse("add.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(symbols) != 1 {
		t.Fatalf("want 1 symbol, got %d: %+v", len(symbols), symbols)
	}
	sym := symbols[0]
	if sym.Name != "Add" {
		t.Errorf("Name = %q, want Add", sym.Name)
	}
	if sym.Kind != "function" {
		t.Errorf("Kind = %q, want function", sym.Kind)
	}
	if sym.StartLine != 3 {
		t.Errorf("StartLine = %d, want 3", sym.StartLine)
	}
	if sym.Signature != "func Add(a, b int) int" {
		t.Errorf("Signature = %q, want %q", sym.Signature, "func Add(a, b int) int")
	}
}

func TestParseGoMethodOnType(t *testing.T) {
	src := []byte("package p\n\ntype T struct{}\n\nfunc (t T) Do() {}\n")
	symbols, _, err := indexing.Parse("t.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var found *indexing.Symbol
	for i := range symbols {
		if symbols[i].Name == "Do" {
			found = &symbols[i]
		}
	}
	if found == nil {
		t.Fatalf("symbol Do not found in %+v", symbols)
	}
	if found.Kind != "method" {
		t.Errorf("Kind = %q, want method", found.Kind)
	}
}

func TestParseRecordsReferences(t *testing.T) {
	callerSrc := []byte("package p\n\nfunc Caller() int {\n\treturn Add(1, 2)\n}\n")
	_, refs, err := indexing.Parse("caller.go", callerSrc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var found *indexing.Reference
	for i := range refs {
		if refs[i].ToName == "Add" {
			found = &refs[i]
		}
	}
	if found == nil {
		t.Fatalf("reference to Add not found in %+v", refs)
	}
	if found.FromPath != "caller.go" {
		t.Errorf("FromPath = %q, want caller.go", found.FromPath)
	}
}

func TestUnsupportedExtensionIsEmpty(t *testing.T) {
	symbols, refs, err := indexing.Parse("notes.txt", []byte("just some notes\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(symbols) != 0 {
		t.Errorf("want no symbols, got %+v", symbols)
	}
	if len(refs) != 0 {
		t.Errorf("want no references, got %+v", refs)
	}
}
