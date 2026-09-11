package gates

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// knownGates is the fixed set of gate names the platform understands. A
// definition or a Work Item requirement naming anything outside this set is
// rejected rather than silently accepted, so enforcement can never be
// weakened by a typo or a made-up gate.
var knownGates = map[string]bool{
	"tests":             true,
	"architecture":      true,
	"security":          true,
	"api-compatibility": true,
	"dependencies":      true,
	"quality":           true,
	"documentation":     true,
}

// gateFile is the YAML shape of one file under .novaforge/gates/.
type gateFile struct {
	Name     string         `yaml:"name"`
	Required bool           `yaml:"required"`
	Params   map[string]any `yaml:"params"`
}

const gatesDir = ".novaforge/gates"

// Resolve reads gate definitions from the repository's .novaforge/gates
// directory at targetRef — the branch being merged into, never the source
// branch — and unions them with workItemGates, the Work Item's required
// gates. Reading only at targetRef is what makes it impossible for a change
// under review to delete or weaken the gates that judge it: the source
// branch is never consulted.
func Resolve(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, targetRef string, workItemGates []string) ([]Definition, error) {
	tree, err := git.GetTree(ctx, &gitv1.GetTreeRequest{
		Repo: repoID.String(),
		Ref:  targetRef,
		Path: gatesDir,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve gate definitions at %s: %w", targetRef, err)
	}

	defs := make(map[string]Definition)
	for _, entry := range tree.Entries {
		if entry.Kind != "blob" {
			continue
		}
		if !strings.HasSuffix(entry.Name, ".yaml") && !strings.HasSuffix(entry.Name, ".yml") {
			continue
		}
		path := gatesDir + "/" + entry.Name
		blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{
			Repo: repoID.String(),
			Ref:  targetRef,
			Path: path,
		})
		if err != nil {
			return nil, fmt.Errorf("read gate config %s: %w", path, err)
		}

		var gf gateFile
		if err := yaml.Unmarshal(blob.Content, &gf); err != nil {
			return nil, fmt.Errorf("malformed gate config %s: %w", path, err)
		}
		if gf.Name == "" {
			return nil, fmt.Errorf("malformed gate config %s: missing gate name", path)
		}
		if !knownGates[gf.Name] {
			return nil, fmt.Errorf("unknown gate %q in %s", gf.Name, path)
		}
		defs[gf.Name] = Definition{Name: gf.Name, Params: gf.Params, Required: gf.Required}
	}

	for _, name := range workItemGates {
		if !knownGates[name] {
			return nil, fmt.Errorf("unknown gate %q required by work item", name)
		}
		d, ok := defs[name]
		if !ok {
			d = Definition{Name: name}
		}
		d.Required = true
		defs[name] = d
	}

	out := make([]Definition, 0, len(defs))
	for _, d := range defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
