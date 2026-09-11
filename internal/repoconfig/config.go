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
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
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
	Name         string   `yaml:"name"`
	DefaultAgent string   `yaml:"default_agent"`
	Gates        []string `yaml:"gates"`
}

// AgentDef is one agent definition read from a file under
// .novaforge/agents/.
type AgentDef struct {
	Name  string   `yaml:"name"`
	Role  string   `yaml:"role"`
	Model string   `yaml:"model"`
	Tools []string `yaml:"tools"`
}

// GateDef is one gate definition read from a file under .novaforge/gates/.
type GateDef struct {
	Name   string         `yaml:"name"`
	Params map[string]any `yaml:"params"`
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
// A repository with no .novaforge/project.yaml returns the zero Config and
// a nil error.
func Load(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, ref string) (Config, error) {
	repo := repoID.String()

	projectBlob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: projectPath})
	if isNotFound(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("novaforge config: read %s: %w", projectPath, err)
	}

	var project Project
	if err := yaml.Unmarshal(projectBlob.Content, &project); err != nil {
		return Config{}, fmt.Errorf("novaforge config: parse %s: %w", projectPath, err)
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

func loadAgents(ctx context.Context, git gitv1.GitServiceClient, repo, ref string) ([]AgentDef, error) {
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
		if err := yaml.Unmarshal(blob.Content, &def); err != nil {
			return nil, fmt.Errorf("novaforge config: parse %s: %w", path, err)
		}
		for _, tool := range def.Tools {
			if !known[tool] {
				return nil, fmt.Errorf("novaforge config: %s: agent %q names unknown tool %q", path, def.Name, tool)
			}
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func loadGates(ctx context.Context, git gitv1.GitServiceClient, repo, ref string) ([]GateDef, error) {
	tree, err := git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: ref, Path: gatesDir})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("novaforge config: list %s: %w", gatesDir, err)
	}

	var defs []GateDef
	for _, entry := range tree.Entries {
		if entry.Kind != "blob" {
			continue
		}
		path := gatesDir + "/" + entry.Name
		blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: path})
		if err != nil {
			return nil, fmt.Errorf("novaforge config: read %s: %w", path, err)
		}
		var def GateDef
		if err := yaml.Unmarshal(blob.Content, &def); err != nil {
			return nil, fmt.Errorf("novaforge config: parse %s: %w", path, err)
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func listContextDocs(ctx context.Context, git gitv1.GitServiceClient, repo, ref string) ([]string, error) {
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
