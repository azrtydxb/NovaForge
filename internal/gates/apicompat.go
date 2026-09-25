package gates

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/novaforge/novaforge/internal/analysis"
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

var errAPISpecMissing = errors.New("OpenAPI spec does not exist at revision")

// Git evidence is read through the injected owner adapter, never a privileged
// local subprocess. Only a confirmed NotFound permits an absent target spec.
func gitShowAtSHA(ctx context.Context, run analysis.Exec, workdir, sha, path string) ([]byte, error) {
	if run == nil || !gateRevision.MatchString(sha) {
		return nil, fmt.Errorf("immutable API evidence executor/revision missing")
	}
	out, exit, err := run(ctx, workdir, "git", "show", sha+":"+path)
	if err != nil {
		return nil, err
	}
	if exit == 44 {
		return nil, errAPISpecMissing
	}
	if exit != 0 {
		return nil, fmt.Errorf("API evidence read failed: exit %d", exit)
	}
	return out, nil
}

// runAPICompat evaluates the api-compatibility gate by diffing
// api/openapi.yaml between TargetSHA and SourceSHA, failing when an existing
// path, method, or required response present at the target is missing at
// the source.
func runAPICompat(ctx context.Context, in Input) (Evaluation, error) {
	targetContent, err := gitShowAtSHA(ctx, in.Exec, in.WorkDir, in.PolicySHA, openapiSpecPath)
	if err != nil {
		if errors.Is(err, errAPISpecMissing) {
			return newEvaluation(in, "api-compatibility", "pass", "no openapi spec at target"), nil
		}
		return toolError(in, "api-compatibility", err)
	}
	sourceContent, err := gitShowAtSHA(ctx, in.Exec, in.WorkDir, in.SourceSHA, openapiSpecPath)
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
