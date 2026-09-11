# intelligence 01: Symbol extraction with Tree-sitter

Status: open
Created: 2026-09-11

## Description

Plan step 1 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/indexing/parse.go`, `internal/indexing/parse_test.go`

Interfaces: produces `indexing.Symbol{Name, Kind, Path string, StartLine, EndLine int, Signature string}`, `indexing.Reference{FromPath string, ToName string, Line int}`, and `indexing.Parse(path string, src []byte) ([]Symbol, []Reference, error)`. `Kind` is one of `function`, `method`, `type`, `const`, or `var`. Languages are selected by file extension, with an unsupported extension returning empty slices and a nil error.

## Acceptance criteria

- [ ] Write the failing test `internal/indexing/parse_test.go`: `func TestParseGoFunctions(t *testing.T)` parses the literal source `package p\n\nfunc Add(a, b int) int { return a + b }\n` and asserts one symbol named `Add`, kind `function`, `StartLine` 3, and signature `func Add(a, b int) int`; `func TestParseGoMethodOnType(t *testing.T)` asserts a method on a receiver is kind `method`; `func TestParseRecordsReferences(t *testing.T)` asserts a call to `Add` in another file produces a `Reference` with `ToName` `Add`; `func TestUnsupportedExtensionIsEmpty(t *testing.T)` asserts `Parse("notes.txt", ...)` returns no symbols and no error. Run `go test ./internal/indexing/` — expect FAIL with "undefined: indexing.Parse".
- [ ] Add `go get github.com/tree-sitter/go-tree-sitter` plus the Go, TypeScript, Python, and Java grammars, and implement `Parse` dispatching on extension with a per-language query naming the capture `@definition` for symbols and `@reference` for call sites.
- [ ] Bound the work: skip any file larger than 2 MiB and any file the grammar reports as having an error node covering more than half its span, so a 50 GB monorepo cannot stall indexing on generated blobs.
- [ ] Run `go test ./internal/indexing/` — expect PASS.
- [ ] Commit as `feat: extract symbols and references with tree-sitter`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
