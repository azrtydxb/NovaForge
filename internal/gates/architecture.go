package gates

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/tools/go/packages"
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
// import graph with golang.org/x/tools/go/packages and matching it against
// the forbidden_dependencies param.
func runArchitecture(ctx context.Context, in Input) (Evaluation, error) {
	forbidden, err := parseForbiddenDeps(paramStringSlice(in.Params, "forbidden_dependencies"))
	if err != nil {
		return newEvaluation(in, "architecture", "error", err.Error()), nil
	}

	cfg := &packages.Config{
		Mode:    packages.NeedName | packages.NeedImports | packages.NeedDeps,
		Dir:     in.WorkDir,
		Context: ctx,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return newEvaluation(in, "architecture", "error", fmt.Sprintf("load packages: %v", err)), nil
	}
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			return newEvaluation(in, "architecture", "error", fmt.Sprintf("load packages: %v", e)), nil
		}
	}

	var violations []string
	for _, pkg := range pkgs {
		for _, imp := range pkg.Imports {
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
