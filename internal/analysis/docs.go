package analysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// UndocumentedExports lists exported top-level Go declarations with no doc
// comment. It is the documentation gate's check: the part of documentation a
// machine can verify without judging prose. It needs no external tool — the
// parser is the standard library's — and it skips tests, generated code and
// vendored modules, none of which a person documents by hand.
func UndocumentedExports(dir string) ([]Finding, error) {
	var found []Finding
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		if ast.IsGenerated(file) || file.Name.Name == "main" {
			return nil
		}
		rel := relative(dir, path)
		for _, decl := range file.Decls {
			found = append(found, undocumented(fset, rel, decl)...)
		}
		return nil
	})
	return found, err
}

func undocumented(fset *token.FileSet, path string, decl ast.Decl) []Finding {
	miss := func(name string, pos token.Pos) Finding {
		return Finding{
			Rule: "undocumented-export", Path: path, Line: fset.Position(pos).Line,
			Severity: "low", Message: name + " is exported without a doc comment",
		}
	}
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Name.IsExported() && d.Doc == nil && receiverExported(d) {
			return []Finding{miss(d.Name.Name, d.Pos())}
		}
	case *ast.GenDecl:
		var out []Finding
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.IsExported() && d.Doc == nil && s.Doc == nil {
					out = append(out, miss(s.Name.Name, s.Pos()))
				}
			case *ast.ValueSpec:
				// A grouped const or var block documented as a whole counts.
				if d.Doc != nil || s.Doc != nil {
					continue
				}
				for _, n := range s.Names {
					if n.IsExported() {
						out = append(out, miss(n.Name, n.Pos()))
					}
				}
			}
		}
		return out
	}
	return nil
}

// receiverExported reports whether a method's receiver type is exported; a
// method on an unexported type is not part of the package's API.
func receiverExported(d *ast.FuncDecl) bool {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return true
	}
	t := d.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if idx, ok := t.(*ast.IndexExpr); ok {
		t = idx.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.IsExported()
	}
	return true
}
