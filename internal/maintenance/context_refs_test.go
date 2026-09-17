package maintenance

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExplicitContextReferences(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".novaforge", "context")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "design.md")
	body := "Plain Gone and `Maybe` are not references. [Gone](symbol:code.go#Gone) [Again](symbol:code.go#Gone) [Space](symbol:space%20dir/code.go#Used)\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	docs, err := contextReferences(dir)
	if err != nil || len(docs) != 1 {
		t.Fatalf("parse references: %+v %v", docs, err)
	}
	if want := []string{"code.go#Gone", "space dir/code.go#Used"}; !reflect.DeepEqual(docs[0].ReferencedSymbols, want) {
		t.Fatalf("references=%v, want %v", docs[0].ReferencedSymbols, want)
	}
	for _, target := range []string{"../code.go#Gone", "code.py#Gone", "code.go#", "code.go#not-a-symbol"} {
		if err := os.WriteFile(file, []byte("[Bad](symbol:"+target+")"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := contextReferences(dir); err == nil {
			t.Fatalf("accepted invalid reference %q", target)
		}
	}
	if docs, err := contextReferences(t.TempDir()); err != nil || len(docs) != 0 {
		t.Fatalf("missing documents fabricated references: %+v %v", docs, err)
	}
}
