package indexing

import (
	"bufio"
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/graph"
)

// pushInfo is what the indexer knows about a push beyond its changed paths:
// the Go module the repository declares, the exact lines each file's change
// touched, and the commit each changed file is attributed to. A direct
// IndexCommit call has none of it, and still writes symbols and dependency
// edges — only changed_by history needs a push.
type pushInfo struct {
	modules *moduleContexts
	changed map[string]map[int]bool
	commits map[string]graph.CommitInfo
}

// attributionCommitLimit bounds how many of a push's commits are inspected
// to attribute each changed file to the commit that last touched it. A push
// importing a repository with a long history would otherwise diff every
// commit it ever had.
const attributionCommitLimit = 50

var (
	hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	agentKeyRe   = regexp.MustCompile(`agents/([A-Z][A-Z0-9]*-\d+)/`)
	workKeyRe    = regexp.MustCompile(`\b([A-Z][A-Z0-9]*-\d+)\b`)
)

// changedLines reads a unified diff and returns, per new-side path, the line
// numbers the change added or modified in the new version, plus the lines on
// either side of a pure deletion. Context lines are not changes: a hunk's
// header range includes three lines of context on each side, and treating
// that range as changed attributed an untouched neighbouring function to a
// commit that never touched it.
func changedLines(unified string) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	var current map[int]bool
	line := 0
	sc := bufio.NewScanner(strings.NewReader(unified))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		text := sc.Text()
		if strings.HasPrefix(text, "diff --git ") {
			current = nil
			line = 0
			continue
		}
		if line == 0 && strings.HasPrefix(text, "+++ ") {
			// This is one destination path, unlike the ambiguous pair of
			// unquoted names in a diff header. Git C-quotes control bytes
			// and non-ASCII names; decode them rather than indexing quotes.
			name := strings.TrimSuffix(strings.TrimPrefix(text, "+++ "), "\t")
			if strings.HasPrefix(name, "\"") {
				decoded, err := strconv.Unquote(name)
				if err != nil {
					continue
				}
				name = decoded
			}
			if strings.HasPrefix(name, "b/") {
				current = map[int]bool{}
				out[strings.TrimPrefix(name, "b/")] = current
			}
			continue
		}
		if current == nil {
			continue
		}
		if m := hunkHeaderRe.FindStringSubmatch(text); m != nil {
			line, _ = strconv.Atoi(m[1])
			continue
		}
		if line == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(text, "+"):
			current[line] = true
			line++
		case strings.HasPrefix(text, "-"):
			current[line] = true
			if line > 1 {
				current[line-1] = true
			}
		case strings.HasPrefix(text, "\\"):
		default:
			line++
		}
	}
	return out
}

// workItemKey extracts the Work Item a commit message names. A merge made
// through the platform is titled after its source branch, and an agent's
// branch is agents/<KEY>/..., so that form is preferred; otherwise the first
// key-shaped token counts. A message naming none yields "" — a commit is
// never attributed to a Work Item it does not name.
func workItemKey(message string) string {
	if m := agentKeyRe.FindStringSubmatch(message); m != nil {
		return m[1]
	}
	if m := workKeyRe.FindStringSubmatch(message); m != nil {
		return m[1]
	}
	return ""
}

// attribute assigns each changed path to the newest commit in the push that
// touched it. Every commit between the push's old and new head is a
// candidate, newest first; a merge commit is diffed against its first
// parent, so a merge claims everything it brought in — and a merge made
// through the platform names the Work Item its branch was for.
//
// When the push carries more commits than attributionCommitLimit, a path
// none of the inspected commits touched is left unattributed rather than
// credited to a commit that may not have changed it.
func (idx *Indexer) attribute(ctx context.Context, repoID uuid.UUID, oldSHA, newSHA string, paths []string) map[string]graph.CommitInfo {
	out := map[string]graph.CommitInfo{}
	if len(paths) == 0 {
		return out
	}
	rng := newSHA
	if !isZeroSHA(oldSHA) {
		rng = oldSHA + ".." + newSHA
	}
	list, err := idx.Git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repoID.String(), Ref: rng, Limit: attributionCommitLimit})
	if err != nil {
		return out
	}
	wanted := make(map[string]bool, len(paths))
	for _, p := range paths {
		wanted[p] = true
	}
	for _, c := range list.GetCommits() {
		if len(out) == len(wanted) {
			break
		}
		diff, err := idx.Git.GetDiff(ctx, &gitv1.GetDiffRequest{Repo: repoID.String(), From: c.GetSha() + "^", To: c.GetSha()})
		if status.Code(err) == codes.NotFound {
			// A root commit has no parent; everything in it is new.
			diff, err = idx.Git.GetDiff(ctx, &gitv1.GetDiffRequest{Repo: repoID.String(), From: emptyTreeSHA, To: c.GetSha()})
		}
		if err != nil || !diff.GetPathsComplete() {
			continue
		}
		at, _ := time.Parse(time.RFC3339, c.GetAt())
		info := graph.CommitInfo{
			SHA:         c.GetSha(),
			Author:      c.GetAuthorName(),
			Message:     c.GetMessage(),
			At:          at,
			WorkItemKey: workItemKey(c.GetMessage()),
		}
		for _, p := range diff.GetChangedPaths() {
			if wanted[p] {
				if _, done := out[p]; !done {
					out[p] = info
				}
			}
		}
	}
	return out
}

// goEdges resolves a Go file's imports and references into the graph's
// by-name form. Resolution is by package directory, which is how Go itself
// scopes names:
//
//   - an unqualified call names something in the file's own package;
//   - pkg.Name, where pkg is one of the file's imports, names something in
//     that package's directory — when the package is inside the repository;
//   - x.Name where x is not an import is a method call, resolved to methods of
//     that name in the file's own package and in the packages it imports.
//
// A reference made from inside a test function (TestX, BenchmarkX, FuzzX,
// ExampleX) is a tested_by relation rather than a dependency; any other
// reference in a test file is test scaffolding and is not recorded.
func goEdges(path, module string, file File, symbolKeys map[*Symbol]string) ([]graph.FileImport, []graph.FileReference) {
	return goEdgesResolved(path, file, symbolKeys, func(importPath string) string { return moduleDir(module, importPath) })
}

func goEdgesResolved(path string, file File, symbolKeys map[*Symbol]string, resolve func(string) string) ([]graph.FileImport, []graph.FileReference) {
	dir := graphDir(path)
	isTest := strings.HasSuffix(path, "_test.go")

	var imports []graph.FileImport
	aliases := map[string]string{} // import name -> directory ("" when external)
	var importedDirs []string
	for _, imp := range file.Imports {
		d := resolve(imp.Path)
		imports = append(imports, graph.FileImport{ImportPath: imp.Path, Dir: d})
		target := ""
		if d != "" {
			target = d
			if d == "." {
				target = ""
			}
			importedDirs = append(importedDirs, target)
		}
		if imp.Name != "_" && imp.Name != "." {
			if d == "" {
				aliases[imp.Name] = "\x00external"
			} else {
				aliases[imp.Name] = target
			}
		}
	}

	seen := map[graph.FileReference]bool{}
	var refs []graph.FileReference
	add := func(r graph.FileReference) {
		if !seen[r] {
			seen[r] = true
			refs = append(refs, r)
		}
	}
	for _, ref := range file.References {
		from := enclosingSymbol(file.Symbols, ref.Line)
		kind := "depends_on"
		fromKey := ""
		if from != nil {
			fromKey = symbolKeys[from]
		}
		if isTest {
			if from == nil || !isTestFunction(from) {
				continue
			}
			kind = "tested_by"
		}
		switch {
		case !ref.Selector:
			add(graph.FileReference{FromKey: fromKey, TargetDir: dir, TargetName: ref.ToName, Kind: kind})
		case ref.Qualifier != "" && aliases[ref.Qualifier] != "":
			target := aliases[ref.Qualifier]
			if target == "\x00external" {
				continue
			}
			add(graph.FileReference{FromKey: fromKey, TargetDir: target, TargetName: ref.ToName, Kind: kind})
		case ref.Qualifier != "" && hasAlias(aliases, ref.Qualifier):
			// An import of the repository's root package.
			add(graph.FileReference{FromKey: fromKey, TargetDir: "", TargetName: ref.ToName, Kind: kind})
		default:
			add(graph.FileReference{FromKey: fromKey, TargetDir: dir, TargetName: ref.ToName, TargetMethod: true, Kind: kind})
			for _, d := range importedDirs {
				add(graph.FileReference{FromKey: fromKey, TargetDir: d, TargetName: ref.ToName, TargetMethod: true, Kind: kind})
			}
		}
	}
	return imports, refs
}

func hasAlias(aliases map[string]string, name string) bool {
	_, ok := aliases[name]
	return ok
}

// moduleDir maps an import path to the directory it names inside a module
// whose path is module: "." for the module's root package, "" for a package
// outside the module.
func moduleDir(module, importPath string) string {
	if module == "" {
		return ""
	}
	if importPath == module {
		return "."
	}
	if strings.HasPrefix(importPath, module+"/") {
		return strings.TrimPrefix(importPath, module+"/")
	}
	return ""
}

// graphDir is the directory attribute symbols carry: "" at the repository
// root.
func graphDir(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}

// enclosingSymbol is the innermost function or method whose lines contain
// line, or nil for a reference outside any.
func enclosingSymbol(symbols []Symbol, line int) *Symbol {
	var best *Symbol
	for i := range symbols {
		s := &symbols[i]
		if s.Kind != "function" && s.Kind != "method" {
			continue
		}
		if line < s.StartLine || line > s.EndLine {
			continue
		}
		if best == nil || s.EndLine-s.StartLine < best.EndLine-best.StartLine {
			best = s
		}
	}
	return best
}

func isTestFunction(s *Symbol) bool {
	if s.Kind != "function" {
		return false
	}
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(s.Name, prefix) {
			return true
		}
	}
	return false
}

// symbolsTouched is the keys of the symbols whose lines include a changed
// line.
func symbolsTouched(symbols []Symbol, keys map[*Symbol]string, lines map[int]bool) map[string]bool {
	out := map[string]bool{}
	for i := range symbols {
		s := &symbols[i]
		for l := s.StartLine; l <= s.EndLine; l++ {
			if lines[l] {
				out[keys[s]] = true
				break
			}
		}
	}
	return out
}
