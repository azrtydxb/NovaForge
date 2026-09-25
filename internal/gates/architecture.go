package gates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
)

// depRule is one parsed "<from> -> <to>" entry from the forbidden_dependencies
// param, matched against declared Go package names.
type depRule struct {
	from, to string
}

// parseForbiddenDeps parses each "<from> -> <to>" rule string.
func parseForbiddenDeps(rules []string) ([]depRule, error) {
	out := make([]depRule, 0, len(rules))
	for _, r := range rules {
		parts := strings.SplitN(r, "->", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid forbidden_dependencies rule %q, want \"<from> -> <to>\"", r)
		}
		out = append(out, depRule{
			from: strings.TrimSpace(parts[0]),
			to:   strings.TrimSpace(parts[1]),
		})
	}
	return out, nil
}

// runArchitecture evaluates the architecture gate by walking the workdir's
// import graph inside the injected sandbox and matching it against
// the forbidden_dependencies param.
func runArchitecture(ctx context.Context, in Input) (Evaluation, error) {
	forbidden, err := parseForbiddenDeps(paramStringSlice(in.Params, "forbidden_dependencies"))
	if err != nil {
		return newEvaluation(in, "architecture", "error", err.Error()), nil
	}

	// A nested module is not covered by root ./...; refuse incomplete evidence.
	if err := filepath.WalkDir(in.WorkDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.Name() == "go.mod" && filepath.Dir(path) != in.WorkDir {
			return fmt.Errorf("nested module architecture evidence unavailable: %s", path)
		}
		return nil
	}); err != nil {
		return toolError(in, "architecture", err)
	}
	if in.Exec == nil {
		return toolError(in, "architecture", fmt.Errorf("isolated executor missing"))
	}
	out, exit, err := in.Exec(ctx, in.WorkDir, "go", "list", "-deps", "-json", "./...")
	if err != nil {
		return toolError(in, "architecture", err)
	}
	if exit != 0 || len(out) > sandboxOutputLimit {
		return toolError(in, "architecture", fmt.Errorf("go list failed or output exceeded bound (exit %d)", exit))
	}
	type listedPackage struct {
		ImportPath string
		Name       string
		Imports    []string
		Error      *struct{ Err string }
		DepsErrors []struct{ Err string }
		Incomplete bool
	}
	pkgs := map[string]listedPackage{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg listedPackage
		err := dec.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return toolError(in, "architecture", fmt.Errorf("invalid go list evidence: %w", err))
		}
		if pkg.Error != nil || len(pkg.DepsErrors) > 0 || pkg.Incomplete || pkg.ImportPath == "" || pkg.Name == "" {
			return toolError(in, "architecture", fmt.Errorf("incomplete package evidence"))
		}
		if _, exists := pkgs[pkg.ImportPath]; exists {
			return toolError(in, "architecture", fmt.Errorf("duplicate package evidence"))
		}
		pkgs[pkg.ImportPath] = pkg
	}
	if len(pkgs) == 0 {
		return toolError(in, "architecture", fmt.Errorf("empty package evidence"))
	}

	var violations []string
	for _, pkg := range pkgs {
		for _, path := range pkg.Imports {
			imp, ok := pkgs[path]
			if !ok {
				return toolError(in, "architecture", fmt.Errorf("missing import evidence for %s", path))
			}
			for _, rule := range forbidden {
				if pkg.Name == rule.from && imp.Name == rule.to {
					violations = append(violations, fmt.Sprintf("%s -> %s", pkg.Name, imp.Name))
				}
			}
		}
	}

	if len(violations) > 0 {
		return newEvaluation(in, "architecture", "fail", fmt.Sprintf("forbidden dependency: %s", strings.Join(violations, ", "))), nil
	}
	return newEvaluation(in, "architecture", "pass", "no forbidden dependencies"), nil
}
