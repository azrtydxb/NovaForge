package semanticindex

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"unicode/utf16"
)

// GoDefinitionQueries selects actual identifier positions, not guessed target
// names. Gopls resolves receiver/module/workspace semantics in the pinned tree.
func GoDefinitionQueries(s Snapshot) ([]Query, error) {
	paths := []string{}
	for p := range s.Files {
		if strings.HasSuffix(p, ".go") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	var out []Query
	for _, p := range paths {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, s.Files[p], 0)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(s.Files[p]), "\n")
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || len(out) > MaxQueries {
				return true
			}
			pos := fset.Position(id.Pos())
			prefix := lines[pos.Line-1][:pos.Column-1]
			out = append(out, Query{Path: p, Language: "go", Position: Position{Line: pos.Line - 1, Character: len(utf16.Encode([]rune(prefix)))}})
			return true
		})
		if len(out) > MaxQueries {
			return nil, fmt.Errorf("Go definition query limit exceeded")
		}
	}
	return out, nil
}
