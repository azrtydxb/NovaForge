package gates

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/approvals"
)

// Requirement is one governed action a change performs, found by reading the
// change itself: which approvals.Action it is, why, and the files that made it
// so.
//
// Every input here is repository content — the diff and the manifests at the
// two refs. Nothing an agent says about its change (a run title, a plan, a
// comment, a claim that "no schema changed") is read, so a model cannot talk
// its way out of an approval: it would have to not make the change.
type Requirement struct {
	Action approvals.Action
	Reason string
	Paths  []string
}

// ClassifyChange reads what sourceRef changes relative to where it branched
// from targetRef and returns the governed actions it performs, in a fixed
// order: gate definitions edited, database schema changed, dependencies
// added.
func ClassifyChange(ctx context.Context, git gitv1.GitServiceClient, repoID uuid.UUID, targetRef, sourceRef string) ([]Requirement, error) {
	diff, err := git.GetDiff(ctx, &gitv1.GetDiffRequest{
		Repo: repoID.String(), From: targetRef, To: sourceRef, MergeBase: true,
	})
	if err != nil {
		return nil, fmt.Errorf("diff %s...%s: %w", targetRef, sourceRef, err)
	}
	changed := ChangedPaths(diff.GetUnified())

	var gateFiles, schemaFiles, manifests []string
	for _, p := range changed {
		switch {
		case isGateConfigPath(p):
			gateFiles = append(gateFiles, p)
		case isSchemaPath(p):
			schemaFiles = append(schemaFiles, p)
		}
		if manifestParser(p) != nil {
			manifests = append(manifests, p)
		}
	}

	var out []Requirement
	if len(gateFiles) > 0 {
		out = append(out, Requirement{
			Action: approvals.ActionChangeGateConfig,
			Reason: "edits gate definitions under " + gatesDir,
			Paths:  gateFiles,
		})
	}
	if len(schemaFiles) > 0 {
		out = append(out, Requirement{
			Action: approvals.ActionChangeDBSchema,
			Reason: "changes the database schema",
			Paths:  schemaFiles,
		})
	}

	var added []string
	var addedIn []string
	for _, m := range manifests {
		before, err := manifestDeps(ctx, git, repoID, targetRef, m)
		if err != nil {
			return nil, err
		}
		after, err := manifestDeps(ctx, git, repoID, sourceRef, m)
		if err != nil {
			return nil, err
		}
		var fresh []string
		for name := range after {
			if !before[name] {
				fresh = append(fresh, name)
			}
		}
		if len(fresh) > 0 {
			sort.Strings(fresh)
			added = append(added, fresh...)
			addedIn = append(addedIn, m)
		}
	}
	if len(added) > 0 {
		out = append(out, Requirement{
			Action: approvals.ActionAddDependency,
			Reason: "adds dependencies: " + strings.Join(added, ", "),
			Paths:  addedIn,
		})
	}
	return out, nil
}

func isGateConfigPath(p string) bool {
	return p == gatesDir || strings.HasPrefix(p, gatesDir+"/")
}

// schemaDirs are directory names that, anywhere in a path, hold schema
// migrations in the ecosystems this platform sees.
var schemaDirs = map[string]bool{"migrations": true, "migration": true, "migrate": true}

// schemaFileNames are files that are the schema itself.
var schemaFileNames = map[string]bool{"schema.prisma": true, "schema.rb": true, "structure.sql": true}

// isSchemaPath reports whether p is database schema. It errs towards asking:
// a .sql file that is only a query costs a person a click, while a migration
// that slips past costs a production database.
func isSchemaPath(p string) bool {
	if strings.HasSuffix(strings.ToLower(p), ".sql") || schemaFileNames[path.Base(p)] {
		return true
	}
	dirs := strings.Split(path.Dir(p), "/")
	for _, d := range dirs {
		if schemaDirs[strings.ToLower(d)] {
			return true
		}
	}
	return strings.Contains(p, "alembic/versions/")
}

// ChangedPaths returns every path a unified git diff touches, both sides of a
// rename included, sorted and unique. Paths are read from each file's header,
// never from hunk content, so a removed line that happens to read
// "-- a/secret" is not mistaken for a file.
func ChangedPaths(unified string) []string {
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || p == "/dev/null" {
			return
		}
		seen[p] = true
	}
	inHeader := false
	sc := bufio.NewScanner(strings.NewReader(unified))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHeader = true
			a, b := splitGitHeader(strings.TrimPrefix(line, "diff --git "))
			add(a)
			add(b)
		case strings.HasPrefix(line, "@@"):
			inHeader = false
		case !inHeader:
			continue
		case strings.HasPrefix(line, "--- "):
			add(stripSide(unquote(strings.TrimPrefix(line, "--- ")), "a/"))
		case strings.HasPrefix(line, "+++ "):
			add(stripSide(unquote(strings.TrimPrefix(line, "+++ ")), "b/"))
		case strings.HasPrefix(line, "rename from "):
			add(unquote(strings.TrimPrefix(line, "rename from ")))
		case strings.HasPrefix(line, "rename to "):
			add(unquote(strings.TrimPrefix(line, "rename to ")))
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// splitGitHeader splits "a/x b/y" (either side possibly quoted). Git writes
// both sides with the same path unless the file was renamed, which the
// rename lines then state unambiguously.
func splitGitHeader(rest string) (string, string) {
	if strings.HasPrefix(rest, `"`) {
		if end := closingQuote(rest); end > 0 {
			a := unquote(rest[:end+1])
			b := unquote(strings.TrimSpace(rest[end+1:]))
			return stripSide(a, "a/"), stripSide(b, "b/")
		}
	}
	if i := strings.Index(rest, " b/"); i >= 0 {
		return stripSide(rest[:i], "a/"), stripSide(unquote(rest[i+1:]), "b/")
	}
	return "", ""
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func stripSide(p, prefix string) string {
	return strings.TrimPrefix(p, prefix)
}

// manifestParser returns the dependency parser for a manifest path, or nil
// when the file declares no dependencies this platform can read.
func manifestParser(p string) func([]byte) (map[string]bool, error) {
	base := path.Base(p)
	switch {
	case base == "go.mod":
		return parseGoMod
	case base == "package.json":
		return parsePackageJSON
	case base == "Cargo.toml":
		return parseCargoToml
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		return parseRequirements
	}
	return nil
}

// manifestDeps reads the dependency names a manifest declares at ref. A
// manifest that does not exist at ref declares nothing.
func manifestDeps(ctx context.Context, git gitv1.GitServiceClient, repoID uuid.UUID, ref, p string) (map[string]bool, error) {
	blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repoID.String(), Ref: ref, Path: p})
	if status.Code(err) == codes.NotFound {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", p, ref, err)
	}
	deps, err := manifestParser(p)(blob.GetContent())
	if err != nil {
		// A manifest that cannot be read cannot be shown to add nothing, so
		// it fails the classification rather than passing it.
		return nil, fmt.Errorf("parse %s at %s: %w", p, ref, err)
	}
	return deps, nil
}

func parseGoMod(content []byte) (map[string]bool, error) {
	deps := map[string]bool{}
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(string(content)))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case inBlock && fields[0] == ")":
			inBlock = false
		case inBlock:
			deps[fields[0]] = true
		case fields[0] == "require" && len(fields) >= 2 && fields[1] == "(":
			inBlock = true
		case fields[0] == "require" && len(fields) >= 3:
			deps[fields[1]] = true
		}
	}
	return deps, sc.Err()
}

func parsePackageJSON(content []byte) (map[string]bool, error) {
	var pkg map[string]json.RawMessage
	if err := json.Unmarshal(content, &pkg); err != nil {
		return nil, err
	}
	deps := map[string]bool{}
	for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		raw, ok := pkg[section]
		if !ok {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", section, err)
		}
		for name := range m {
			deps[name] = true
		}
	}
	return deps, nil
}

func parseRequirements(content []byte) (map[string]bool, error) {
	deps := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(content)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		end := strings.IndexAny(line, "=<>!~[;@ ")
		if end >= 0 {
			line = line[:end]
		}
		name := strings.ReplaceAll(strings.ToLower(line), "_", "-")
		if name != "" {
			deps[name] = true
		}
	}
	return deps, sc.Err()
}

func parseCargoToml(content []byte) (map[string]bool, error) {
	deps := map[string]bool{}
	section := ""
	sc := bufio.NewScanner(strings.NewReader(string(content)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			// [dependencies.serde] declares serde as a table.
			for _, kind := range []string{"dependencies.", "dev-dependencies.", "build-dependencies."} {
				if i := strings.Index(section, kind); i >= 0 {
					deps[section[i+len(kind):]] = true
				}
			}
			continue
		}
		if !isCargoDepSection(section) {
			continue
		}
		if eq := strings.Index(line, "="); eq > 0 {
			deps[strings.Trim(strings.TrimSpace(line[:eq]), `"`)] = true
		}
	}
	return deps, sc.Err()
}

func isCargoDepSection(section string) bool {
	for _, kind := range []string{"dependencies", "dev-dependencies", "build-dependencies"} {
		if section == kind || strings.HasSuffix(section, "."+kind) {
			return true
		}
	}
	return false
}
