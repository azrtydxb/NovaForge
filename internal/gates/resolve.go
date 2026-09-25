package gates

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/gatenames"
)

// knownGates is the fixed set of gate names the platform understands. A
// definition or a Work Item requirement naming anything outside this set is
// rejected rather than silently accepted, so enforcement can never be
// weakened by a typo or a made-up gate.
var knownGates = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range gatenames.All() {
		m[n] = true
	}
	return m
}()

// gateFile is the YAML shape of one file under .novaforge/gates/.
type gateFile struct {
	Name     string         `yaml:"name"`
	Required bool           `yaml:"required"`
	Params   map[string]any `yaml:"params"`
}

const gatesDir = ".novaforge/gates"

// Missing required is not explicit optional policy: a misspelled field must
// never silently weaken a merge gate. Decode one complete document only.
func decodeGateFile(content []byte) (gateFile, error) {
	var wire struct {
		Name     string         `yaml:"name"`
		Required *bool          `yaml:"required"`
		Params   map[string]any `yaml:"params"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(&wire); err != nil {
		return gateFile{}, err
	}
	if wire.Required == nil {
		return gateFile{}, fmt.Errorf("required must be explicitly true or false")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return gateFile{}, fmt.Errorf("exactly one YAML document is required")
	}
	return gateFile{Name: wire.Name, Required: *wire.Required, Params: wire.Params}, nil
}

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
	var entries []*gitv1.TreeEntry
	switch {
	case err == nil:
		entries = tree.Entries
	case status.Code(err) == codes.NotFound:
		// No .novaforge/gates directory is a repository that declares no
		// gates — every repository starts that way — provided the target
		// branch itself exists. Returning the NotFound made the merger report
		// the gate controller unreachable, so such a repository could never
		// merge anything. Any other error is still an error: the controller
		// fails closed on everything it cannot read.
		if _, rootErr := git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repoID.String(), Ref: targetRef}); rootErr != nil {
			return nil, fmt.Errorf("resolve gate definitions at %s: %w", targetRef, rootErr)
		}
	default:
		return nil, fmt.Errorf("resolve gate definitions at %s: %w", targetRef, err)
	}

	defs := make(map[string]Definition)
	for _, entry := range entries {
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

		gf, err := decodeGateFile(blob.Content)
		if err != nil {
			return nil, fmt.Errorf("malformed gate config %s: %w", path, err)
		}
		if gf.Name == "" {
			return nil, fmt.Errorf("malformed gate config %s: missing gate name", path)
		}
		if !knownGates[gf.Name] {
			return nil, fmt.Errorf("unknown gate %q in %s", gf.Name, path)
		}
		if _, exists := defs[gf.Name]; exists {
			return nil, fmt.Errorf("duplicate gate %q in %s", gf.Name, path)
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
