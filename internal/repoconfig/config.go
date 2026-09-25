// Package repoconfig loads a repository's .novaforge directory: project
// configuration, agent definitions, gate definitions, and the list of
// context documents. All of it is read through the git service's gRPC
// client, at a specific ref, exactly like any other file in the
// repository — .novaforge is ordinary content under source control, not a
// side channel.
//
// Malformed YAML fails the whole Load loudly rather than returning a
// partially populated Config: a repository's agent and gate behavior must
// never be silently narrowed by a typo. An absent .novaforge directory,
// conversely, is not an error at all — it simply means the repository has
// no NovaForge-specific configuration yet.
package repoconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/gateconfig"
	"github.com/novaforge/novaforge/internal/gatenames"
	"github.com/novaforge/novaforge/internal/tools"
)

const (
	projectPath = ".novaforge/project.yaml"
	agentsDir   = ".novaforge/agents"
	gatesDir    = ".novaforge/gates"
	contextDir  = ".novaforge/context"
)

// Project is the repository-wide configuration read from
// .novaforge/project.yaml.
type Project struct {
	Name string `yaml:"name"`
	// Description is what every agent working on the repository is told
	// about it before its first turn.
	Description  string   `yaml:"description"`
	DefaultAgent string   `yaml:"default_agent"`
	Gates        []string `yaml:"gates"`
}

// AgentDef is one agent definition read from a file under
// .novaforge/agents/.
type AgentDef struct {
	// Path is the file the definition was read from; it is not a YAML field.
	Path string `yaml:"-"`

	Name  string `yaml:"name"`
	Role  string `yaml:"role"`
	Model string `yaml:"model"`
	// Tools, when present, is the complete set of tools the agent is
	// offered. A definition that omits the key leaves the agent every tool;
	// one that lists none ("tools: []") leaves it none.
	Tools        []string `yaml:"tools"`
	Instructions string   `yaml:"instructions"`
	Budget       Budget   `yaml:"budget"`
}

// Budget is a repository's limits for an agent's runs. A zero field sets no
// limit of its own.
type Budget struct {
	WallclockSeconds int64 `yaml:"wallclock_seconds"`
	Tokens           int64 `yaml:"tokens"`
	CostMicros       int64 `yaml:"cost_micros"`
}

// decodeStrict parses YAML refusing keys the target does not declare. A
// governance file with a misspelt key ("tool:" for "tools:") used to parse
// cleanly into a definition that restricted nothing — the silent disabling of
// enforcement a malformed configuration must never cause.
func decodeStrict(content []byte, v any) error {
	// A present null/empty document must not become an unrestricted zero
	// definition. Node.Decode does not support KnownFields, so retain the
	// strict typed pass below after checking the document's root shape.
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("configuration must be a YAML mapping")
	}
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	// A decoder otherwise accepts a valid first document while silently
	// ignoring a later policy, including an empty document or malformed YAML.
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("exactly one YAML document is required")
	}
	return nil
}

// GitReader is the part of the git service Load reads through.
type GitReader interface {
	GetBlob(ctx context.Context, in *gitv1.GetBlobRequest, opts ...grpc.CallOption) (*gitv1.GetBlobResponse, error)
	GetTree(ctx context.Context, in *gitv1.GetTreeRequest, opts ...grpc.CallOption) (*gitv1.GetTreeResponse, error)
}

// ReadContextDoc reads one context document at ref.
func ReadContextDoc(ctx context.Context, git GitReader, repoID uuid.UUID, ref, path string) (string, error) {
	blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repoID.String(), Ref: ref, Path: path})
	if err != nil {
		return "", fmt.Errorf("novaforge config: read %s: %w", path, err)
	}
	return string(blob.GetContent()), nil
}

// AgentFor picks the definition that governs an agent: the one naming it,
// else the one for its role, else the project's default agent. It returns
// nil when the repository defines none of those.
func (c Config) AgentFor(name, role string) *AgentDef {
	for i := range c.Agents {
		if c.Agents[i].Name != "" && c.Agents[i].Name == name {
			return &c.Agents[i]
		}
	}
	for i := range c.Agents {
		if c.Agents[i].Role != "" && c.Agents[i].Role == role {
			return &c.Agents[i]
		}
	}
	if c.Project.DefaultAgent != "" {
		for i := range c.Agents {
			if c.Agents[i].Name == c.Project.DefaultAgent {
				return &c.Agents[i]
			}
		}
	}
	return nil
}

// GateDef is one gate definition read from a file under .novaforge/gates/.
type GateDef struct {
	Name     string         `yaml:"name"`
	Required bool           `yaml:"required"`
	Params   map[string]any `yaml:"params"`
}

// Config is everything Load assembles from one repository's .novaforge
// directory at one ref. Its zero value (returned, with a nil error, when
// the repository has no .novaforge directory) has no project, no agents,
// no gates, and no context documents.
type Config struct {
	Project     Project
	Agents      []AgentDef
	Gates       []GateDef
	ContextDocs []string
}

// Load reads orgID/repoID's .novaforge directory at ref through git,
// parsing project.yaml, every file under agents/ and gates/, and listing
// (without reading) every file under context/. Every agent's Tools list is
// validated against tools.KnownToolNames — the platform's actual registered
// tool set — so configuration can never name a tool NovaForge doesn't
// implement.
//
// A YAML parse failure anywhere returns a wrapped error containing
// "novaforge config" and the zero Config: never a partially populated one.
// So does a key the file's shape does not declare. A repository with no
// .novaforge directory returns the zero Config and a nil error.
func Load(ctx context.Context, git GitReader, orgID, repoID uuid.UUID, ref string) (Config, error) {
	repo := repoID.String()

	// Missing project.yaml does not lift independently declared agent or gate
	// restrictions: continue loading those files even with a zero Project.
	project, err := LoadProject(ctx, git, repoID, ref)
	if err != nil {
		return Config{}, err
	}

	agentDefs, err := loadAgents(ctx, git, repo, ref)
	if err != nil {
		return Config{}, err
	}

	gateDefs, err := loadGates(ctx, git, repo, ref)
	if err != nil {
		return Config{}, err
	}

	contextDocs, err := listContextDocs(ctx, git, repo, ref)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Project:     project,
		Agents:      agentDefs,
		Gates:       gateDefs,
		ContextDocs: contextDocs,
	}, nil
}

// LoadProject reads only project.yaml at the supplied revision. Governance
// callers use the pinned target revision, so a source edit cannot weaken policy.
// It shares the same strict decoder and name checks as the complete loader.
func LoadProject(ctx context.Context, git GitReader, repoID uuid.UUID, ref string) (Project, error) {
	var project Project
	blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repoID.String(), Ref: ref, Path: projectPath})
	if isNotFound(err) {
		return project, nil
	}
	if err != nil {
		return Project{}, fmt.Errorf("novaforge config: read %s: %w", projectPath, err)
	}
	if blob == nil {
		return Project{}, fmt.Errorf("novaforge config: missing response for %s", projectPath)
	}
	if err := decodeStrict(blob.Content, &project); err != nil {
		return Project{}, fmt.Errorf("novaforge config: parse %s: %w", projectPath, err)
	}
	seen := make(map[string]bool)
	for _, name := range project.Gates {
		if !gatenames.Known(name) || seen[name] {
			return Project{}, fmt.Errorf("novaforge config: %s: unknown or duplicate required gate %q", projectPath, name)
		}
		seen[name] = true
	}
	return project, nil
}

func loadAgents(ctx context.Context, git GitReader, repo, ref string) ([]AgentDef, error) {
	tree, err := git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: ref, Path: agentsDir})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("novaforge config: list %s: %w", agentsDir, err)
	}

	known := knownToolSet()
	var defs []AgentDef
	for _, entry := range tree.Entries {
		if entry.Kind != "blob" {
			continue
		}
		path := agentsDir + "/" + entry.Name
		blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: path})
		if err != nil {
			return nil, fmt.Errorf("novaforge config: read %s: %w", path, err)
		}
		var def AgentDef
		if err := decodeStrict(blob.Content, &def); err != nil {
			return nil, fmt.Errorf("novaforge config: parse %s: %w", path, err)
		}
		def.Path = path
		for _, tool := range def.Tools {
			_, external := tools.MCPToolSelection(tool)
			if !known[tool] && !external {
				return nil, fmt.Errorf("novaforge config: %s: agent %q names unknown tool %q", path, def.Name, tool)
			}
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func loadGates(ctx context.Context, git GitReader, repo, ref string) ([]GateDef, error) {
	tree, err := git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: ref, Path: gatesDir})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("novaforge config: list %s: %w", gatesDir, err)
	}

	var defs []GateDef
	seen := make(map[string]bool)
	for _, entry := range tree.Entries {
		if entry.Kind != "blob" {
			continue
		}
		path := gatesDir + "/" + entry.Name
		blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: path})
		if err != nil {
			return nil, fmt.Errorf("novaforge config: read %s: %w", path, err)
		}
		// Match the gate owner's wire policy: omission is not an explicit
		// optional gate. A pointer distinguishes false from missing/null.
		var wire struct {
			Name     string         `yaml:"name"`
			Required *bool          `yaml:"required"`
			Params   map[string]any `yaml:"params"`
		}
		if err := decodeStrict(blob.Content, &wire); err != nil {
			return nil, fmt.Errorf("novaforge config: parse %s: %w", path, err)
		}
		if wire.Required == nil || !gatenames.Known(wire.Name) {
			return nil, fmt.Errorf("novaforge config: %s: known gate name and explicit required boolean are mandatory", path)
		}
		if seen[wire.Name] {
			return nil, fmt.Errorf("novaforge config: %s: duplicate gate %q", path, wire.Name)
		}
		if err := gateconfig.ValidateParameters(wire.Name, wire.Params); err != nil {
			return nil, fmt.Errorf("novaforge config: %s: %w", path, err)
		}
		seen[wire.Name] = true
		defs = append(defs, GateDef{Name: wire.Name, Required: *wire.Required, Params: wire.Params})
	}
	return defs, nil
}

func listContextDocs(ctx context.Context, git GitReader, repo, ref string) ([]string, error) {
	tree, err := git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: ref, Path: contextDir})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("novaforge config: list %s: %w", contextDir, err)
	}
	var docs []string
	for _, entry := range tree.Entries {
		if entry.Kind != "blob" {
			continue
		}
		docs = append(docs, contextDir+"/"+entry.Name)
	}
	return docs, nil
}

func knownToolSet() map[string]bool {
	set := make(map[string]bool)
	for _, name := range tools.KnownToolNames() {
		set[name] = true
	}
	return set
}

// isNotFound reports whether err represents "this path does not exist"
// rather than some other failure: a nil err is never not-found, and any
// other error (including a nil-safe gRPC status of a non-NotFound code) is
// treated as a real failure rather than papered over as an absent path.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return status.Code(err) == codes.NotFound
}
