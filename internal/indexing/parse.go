// Package indexing extracts symbols and references from source files with
// Tree-sitter so the engineering graph can be built without ever dumping a
// whole repository into a model.
package indexing

import (
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// maxFileSize bounds the work Parse will do: a file larger than this is
// skipped so a 50 GB monorepo cannot stall indexing on generated blobs.
const maxFileSize = 2 * 1024 * 1024 // 2 MiB

// Symbol is a named, located definition extracted from a source file.
type Symbol struct {
	Name      string
	Kind      string // function, method, type, const, or var
	Path      string
	StartLine int
	EndLine   int
	Signature string
}

// Reference is a site naming another symbol: a call, or (in Go) a type
// named through its package.
type Reference struct {
	FromPath string
	ToName   string
	Line     int

	// Qualifier is the identifier before the dot in "x.Name" — an import's
	// name or a variable, which only the file's imports can tell apart. It is
	// empty for an unqualified call.
	Qualifier string
	// Selector is true for any "operand.Name" reference, including ones whose
	// operand is not a plain identifier ("a.b.Method()"), which can only be
	// method calls.
	Selector bool
}

// Import is one import declaration of a source file.
type Import struct {
	// Name is the name the file refers to the package by: the explicit alias
	// when there is one, otherwise the last element of Path.
	Name string
	Path string
}

// File is everything ParseFile extracts from one source file.
type File struct {
	// Complete distinguishes a successful supported parse from a deliberate
	// skip or a syntax-error recovery. Absence-based consumers require it.
	Complete   bool
	Symbols    []Symbol
	References []Reference
	Imports    []Import
}

// langSpec configures extraction for one language.
type langSpec struct {
	language    *sitter.Language
	definitionQ string
	// referenceQ captures references. A capture named "reference" is the
	// referenced identifier itself; "selector" is a whole selector expression
	// (operand.field) and "qualified" a Go qualified type (pkg.Type), both
	// taken apart so the qualifier is kept.
	referenceQ  string
	importQ     string
	extractDefs func(node *sitter.Node, src []byte, path string, out *[]Symbol)
}

var extByLang = map[string]*langSpec{}

func init() {
	extByLang[".go"] = goSpec()
	extByLang[".py"] = pythonSpec()
	extByLang[".java"] = javaSpec()
	extByLang[".ts"] = typescriptSpec()
	extByLang[".tsx"] = typescriptSpec()
}

// Parse extracts symbols and references from src, a file at path. An
// extension with no configured language returns empty slices and a nil
// error, never a failure — an unrecognised file is simply not indexed.
func Parse(path string, src []byte) ([]Symbol, []Reference, error) {
	f, err := ParseFile(path, src)
	return f.Symbols, f.References, err
}

// ParseFile is Parse with the file's imports as well, which the indexer
// needs to tell a package-qualified reference from a method call and to know
// which directory the package it names lives in.
func ParseFile(path string, src []byte) (File, error) {
	if len(src) > maxFileSize {
		return File{}, nil
	}

	spec, ok := extByLang[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return File{}, nil
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(spec.language); err != nil {
		return File{}, nil
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return File{}, nil
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		return File{}, nil
	}
	if errorNodeSpan(root) > uint(len(src))/2 {
		// The grammar could not make sense of more than half the file —
		// likely a generated blob or a file in an unrelated dialect. Skip
		// it rather than index garbage.
		return File{}, nil
	}

	complete := !root.HasError()
	var symbols []Symbol
	if spec.definitionQ != "" {
		q, qerr := sitter.NewQuery(spec.language, spec.definitionQ)
		complete = complete && qerr == nil
		if qerr == nil {
			defer q.Close()
			cursor := sitter.NewQueryCursor()
			defer cursor.Close()
			matches := cursor.Matches(q, root, src)
			for match := matches.Next(); match != nil; match = matches.Next() {
				for _, cap := range match.Captures {
					node := cap.Node
					spec.extractDefs(&node, src, path, &symbols)
				}
			}
		}
	}

	var refs []Reference
	if spec.referenceQ != "" {
		q, qerr := sitter.NewQuery(spec.language, spec.referenceQ)
		complete = complete && qerr == nil
		if qerr == nil {
			defer q.Close()
			cursor := sitter.NewQueryCursor()
			defer cursor.Close()
			names := q.CaptureNames()
			matches := cursor.Matches(q, root, src)
			for match := matches.Next(); match != nil; match = matches.Next() {
				for _, cap := range match.Captures {
					node := cap.Node
					if ref, ok := referenceFrom(names[cap.Index], &node, src, path); ok {
						refs = append(refs, ref)
					}
				}
			}
		}
	}

	var imports []Import
	if spec.importQ != "" {
		q, qerr := sitter.NewQuery(spec.language, spec.importQ)
		complete = complete && qerr == nil
		if qerr == nil {
			defer q.Close()
			cursor := sitter.NewQueryCursor()
			defer cursor.Close()
			matches := cursor.Matches(q, root, src)
			for match := matches.Next(); match != nil; match = matches.Next() {
				for _, cap := range match.Captures {
					node := cap.Node
					if imp, ok := goImport(&node, src); ok {
						imports = append(imports, imp)
					}
				}
			}
		}
	}

	return File{Complete: complete, Symbols: symbols, References: refs, Imports: imports}, nil
}

// referenceFrom turns one reference capture into a Reference. A selector or
// qualified type is taken apart here rather than captured field by field,
// because a query capturing both halves separately loses which operand
// belonged to which field.
func referenceFrom(capture string, node *sitter.Node, src []byte, path string) (Reference, bool) {
	line := int(node.StartPosition().Row) + 1
	switch capture {
	case "selector":
		field := node.ChildByFieldName("field")
		operand := node.ChildByFieldName("operand")
		if field == nil || operand == nil {
			return Reference{}, false
		}
		ref := Reference{FromPath: path, ToName: field.Utf8Text(src), Line: line, Selector: true}
		if operand.Kind() == "identifier" {
			ref.Qualifier = operand.Utf8Text(src)
		}
		return ref, true
	case "qualified":
		pkg := node.ChildByFieldName("package")
		name := node.ChildByFieldName("name")
		if pkg == nil || name == nil {
			return Reference{}, false
		}
		return Reference{FromPath: path, ToName: name.Utf8Text(src), Line: line, Qualifier: pkg.Utf8Text(src), Selector: true}, true
	default:
		return Reference{FromPath: path, ToName: node.Utf8Text(src), Line: line}, true
	}
}

// goImport reads one import_spec. The name a file uses for an unaliased
// import is the imported package's own name, which only that package's
// source declares; the last path element is what it is by convention, with a
// trailing major-version element ("/v2") skipped as Go's module rules do.
func goImport(node *sitter.Node, src []byte) (Import, bool) {
	pathNode := node.ChildByFieldName("path")
	if pathNode == nil {
		return Import{}, false
	}
	importPath := strings.Trim(pathNode.Utf8Text(src), "\"`")
	if importPath == "" {
		return Import{}, false
	}
	name := ""
	if n := node.ChildByFieldName("name"); n != nil {
		name = n.Utf8Text(src)
	}
	if name == "" {
		elems := strings.Split(importPath, "/")
		name = elems[len(elems)-1]
		if len(elems) > 1 && isMajorVersion(name) {
			name = elems[len(elems)-2]
		}
		name = strings.TrimSuffix(strings.TrimPrefix(name, "go-"), ".go")
		if i := strings.IndexByte(name, '.'); i > 0 {
			name = name[:i]
		}
	}
	return Import{Name: name, Path: importPath}, true
}

func isMajorVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// errorNodeSpan sums the byte length of every ERROR node in the tree so
// Parse can bail out on files the grammar mostly failed to understand.
func errorNodeSpan(root *sitter.Node) uint {
	var total uint
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.IsError() {
			s, e := n.ByteRange()
			total += e - s
			return
		}
		cursor := n.Walk()
		defer cursor.Close()
		for _, child := range n.Children(cursor) {
			c := child
			walk(&c)
		}
	}
	walk(root)
	return total
}

// signatureFromNode returns the source text of node up to (but excluding)
// its body child, trimmed of trailing whitespace, or the whole node's text
// when it has no body.
func signatureFromNode(node *sitter.Node, src []byte) string {
	body := node.ChildByFieldName("body")
	start := node.StartByte()
	end := node.EndByte()
	if body != nil {
		end = body.StartByte()
	}
	if end > uint(len(src)) {
		end = uint(len(src))
	}
	if start > end {
		start = end
	}
	return strings.TrimRight(string(src[start:end]), " \t\r\n")
}

func nodeLines(node *sitter.Node) (start, end int) {
	return int(node.StartPosition().Row) + 1, int(node.EndPosition().Row) + 1
}

// --- Go -------------------------------------------------------------------

func goSpec() *langSpec {
	return &langSpec{
		language: sitter.NewLanguage(golang.Language()),
		definitionQ: `
			(function_declaration) @definition
			(method_declaration) @definition
			(type_declaration) @definition
			(const_declaration) @definition
			(var_declaration) @definition
		`,
		referenceQ: `
			(call_expression function: (identifier) @reference)
			(call_expression function: (selector_expression) @selector)
			(qualified_type) @qualified
		`,
		importQ:     `(import_spec) @import`,
		extractDefs: extractGoDefs,
	}
}

func extractGoDefs(node *sitter.Node, src []byte, path string, out *[]Symbol) {
	switch node.Kind() {
	case "function_declaration":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: "function", Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	case "method_declaration":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: "method", Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	case "type_declaration":
		cursor := node.Walk()
		defer cursor.Close()
		for _, spec := range node.NamedChildren(cursor) {
			if spec.Kind() != "type_spec" {
				continue
			}
			name := spec.ChildByFieldName("name")
			if name == nil {
				continue
			}
			start, end := nodeLines(&spec)
			*out = append(*out, Symbol{
				Name: name.Utf8Text(src), Kind: "type", Path: path,
				StartLine: start, EndLine: end,
				Signature: "type " + strings.TrimRight(spec.Utf8Text(src), " \t\r\n"),
			})
		}
	case "const_declaration", "var_declaration":
		kind := "const"
		specKind := "const_spec"
		if node.Kind() == "var_declaration" {
			kind = "var"
			specKind = "var_spec"
		}
		cursor := node.Walk()
		defer cursor.Close()
		for _, spec := range node.NamedChildren(cursor) {
			if spec.Kind() != specKind {
				continue
			}
			specCursor := spec.Walk()
			names := spec.ChildrenByFieldName("name", specCursor)
			specCursor.Close()
			start, end := nodeLines(&spec)
			sig := kind + " " + strings.TrimRight(spec.Utf8Text(src), " \t\r\n")
			for _, n := range names {
				*out = append(*out, Symbol{
					Name: n.Utf8Text(src), Kind: kind, Path: path,
					StartLine: start, EndLine: end, Signature: sig,
				})
			}
		}
	}
}

// --- Python -----------------------------------------------------------------

func pythonSpec() *langSpec {
	return &langSpec{
		language: sitter.NewLanguage(python.Language()),
		definitionQ: `
			(function_definition) @definition
			(class_definition) @definition
		`,
		referenceQ: `
			(call function: (identifier) @reference)
			(call function: (attribute attribute: (identifier) @reference))
		`,
		extractDefs: extractPythonDefs,
	}
}

func extractPythonDefs(node *sitter.Node, src []byte, path string, out *[]Symbol) {
	switch node.Kind() {
	case "function_definition":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		kind := "function"
		if parent := node.Parent(); parent != nil && parent.Kind() == "block" {
			if grand := parent.Parent(); grand != nil && grand.Kind() == "class_definition" {
				kind = "method"
			}
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: kind, Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	case "class_definition":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: "type", Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	}
}

// --- Java -------------------------------------------------------------------

func javaSpec() *langSpec {
	return &langSpec{
		language: sitter.NewLanguage(java.Language()),
		definitionQ: `
			(class_declaration) @definition
			(interface_declaration) @definition
			(method_declaration) @definition
			(field_declaration) @definition
		`,
		referenceQ: `
			(method_invocation name: (identifier) @reference)
		`,
		extractDefs: extractJavaDefs,
	}
}

func extractJavaDefs(node *sitter.Node, src []byte, path string, out *[]Symbol) {
	switch node.Kind() {
	case "class_declaration", "interface_declaration":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: "type", Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	case "method_declaration":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: "method", Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	case "field_declaration":
		cursor := node.Walk()
		declarators := node.ChildrenByFieldName("declarator", cursor)
		cursor.Close()
		start, end := nodeLines(node)
		sig := strings.TrimRight(node.Utf8Text(src), " \t\r\n;")
		for _, d := range declarators {
			name := d.ChildByFieldName("name")
			if name == nil {
				continue
			}
			*out = append(*out, Symbol{
				Name: name.Utf8Text(src), Kind: "var", Path: path,
				StartLine: start, EndLine: end, Signature: sig,
			})
		}
	}
}

// --- TypeScript ---------------------------------------------------------

func typescriptSpec() *langSpec {
	return &langSpec{
		language: sitter.NewLanguage(typescript.LanguageTypescript()),
		definitionQ: `
			(function_declaration) @definition
			(method_definition) @definition
			(class_declaration) @definition
			(interface_declaration) @definition
		`,
		referenceQ: `
			(call_expression function: (identifier) @reference)
		`,
		extractDefs: extractTypeScriptDefs,
	}
}

func extractTypeScriptDefs(node *sitter.Node, src []byte, path string, out *[]Symbol) {
	switch node.Kind() {
	case "function_declaration":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: "function", Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	case "method_definition":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: "method", Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	case "class_declaration", "interface_declaration":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		start, end := nodeLines(node)
		*out = append(*out, Symbol{
			Name: name.Utf8Text(src), Kind: "type", Path: path,
			StartLine: start, EndLine: end, Signature: signatureFromNode(node, src),
		})
	}
}
