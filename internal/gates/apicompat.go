package gates

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

// openapiDoc is the subset of an OpenAPI document apicompat cares about:
// which paths exist, which methods each path supports, and which response
// statuses each method declares.
type openapiDoc struct {
	Paths map[string]map[string]struct {
		Responses map[string]any `yaml:"responses"`
	} `yaml:"paths"`
}

const openapiSpecPath = "api/openapi.yaml"

// gitShowAtSHA reads path as it existed at sha inside the git repository
// checked out at workdir, by shelling out to the git binary — per the
// platform's rule that Git is implemented by shelling out rather than a
// pure-Go library.
func gitShowAtSHA(ctx context.Context, workdir, sha, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", workdir, "show", sha+":"+path)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w", sha, path, err)
	}
	return out, nil
}

// runAPICompat evaluates the api-compatibility gate by diffing
// api/openapi.yaml between TargetSHA and SourceSHA, failing when an existing
// path, method, or required response present at the target is missing at
// the source.
func runAPICompat(ctx context.Context, in Input) (Evaluation, error) {
	targetContent, err := gitShowAtSHA(ctx, in.WorkDir, in.TargetSHA, openapiSpecPath)
	if err != nil {
		// No spec at the target to break compatibility with.
		return newEvaluation(in, "api-compatibility", "pass", "no openapi spec at target"), nil
	}
	sourceContent, err := gitShowAtSHA(ctx, in.WorkDir, in.SourceSHA, openapiSpecPath)
	if err != nil {
		return newEvaluation(in, "api-compatibility", "error", fmt.Sprintf("read source openapi spec: %v", err)), nil
	}

	var target, source openapiDoc
	if err := yaml.Unmarshal(targetContent, &target); err != nil {
		return newEvaluation(in, "api-compatibility", "error", fmt.Sprintf("parse target openapi spec: %v", err)), nil
	}
	if err := yaml.Unmarshal(sourceContent, &source); err != nil {
		return newEvaluation(in, "api-compatibility", "error", fmt.Sprintf("parse source openapi spec: %v", err)), nil
	}

	var breaks []string
	for path, methods := range target.Paths {
		sourceMethods, ok := source.Paths[path]
		if !ok {
			breaks = append(breaks, fmt.Sprintf("path %s removed", path))
			continue
		}
		for method, op := range methods {
			sourceOp, ok := sourceMethods[method]
			if !ok {
				breaks = append(breaks, fmt.Sprintf("%s %s removed", strings.ToUpper(method), path))
				continue
			}
			for status := range op.Responses {
				if _, ok := sourceOp.Responses[status]; !ok {
					breaks = append(breaks, fmt.Sprintf("%s %s response %s removed", strings.ToUpper(method), path, status))
				}
			}
		}
	}

	if len(breaks) > 0 {
		return newEvaluation(in, "api-compatibility", "fail", strings.Join(breaks, "; ")), nil
	}
	return newEvaluation(in, "api-compatibility", "pass", "no breaking api changes"), nil
}
