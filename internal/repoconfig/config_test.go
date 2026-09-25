package repoconfig_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/repoconfig"
)

// stubGitClient is an in-process test double for gitv1.GitServiceClient: it
// serves blobs and tree listings from fixed maps rather than a real
// git-platform service.
type stubGitClient struct {
	gitv1.GitServiceClient // embedded nil: only the methods repoconfig.Load
	// actually calls are overridden below; any other call panics via a nil
	// pointer dereference, which is the point — it would mean Load reached
	// outside its documented surface.

	blobs map[string][]byte
	trees map[string][]*gitv1.TreeEntry
}

func notFound() error {
	return status.Error(codes.NotFound, "not found")
}

func (s *stubGitClient) GetBlob(ctx context.Context, in *gitv1.GetBlobRequest, opts ...grpc.CallOption) (*gitv1.GetBlobResponse, error) {
	content, ok := s.blobs[in.Path]
	if !ok {
		return nil, notFound()
	}
	return &gitv1.GetBlobResponse{Content: content}, nil
}

func (s *stubGitClient) GetTree(ctx context.Context, in *gitv1.GetTreeRequest, opts ...grpc.CallOption) (*gitv1.GetTreeResponse, error) {
	entries, ok := s.trees[in.Path]
	if !ok {
		return nil, notFound()
	}
	return &gitv1.GetTreeResponse{Entries: entries}, nil
}

func TestLoadParsesProjectAndAgents(t *testing.T) {
	client := &stubGitClient{
		blobs: map[string][]byte{
			".novaforge/project.yaml": []byte(`
name: example
default_agent: reviewer
gates:
  - tests
  - security
`),
			".novaforge/agents/reviewer.yaml": []byte(`
name: reviewer
role: reviewer
model: anthropic/claude-sonnet-5
tools:
  - repo.search
  - repo.read_file
  - work.comment
`),
		},
		trees: map[string][]*gitv1.TreeEntry{
			".novaforge/agents": {
				{Kind: "blob", Name: "reviewer.yaml"},
			},
		},
	}

	cfg, err := repoconfig.Load(context.Background(), client, uuid.New(), uuid.New(), "main")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Project.Name != "example" {
		t.Fatalf("Project.Name = %q, want %q", cfg.Project.Name, "example")
	}
	if cfg.Project.DefaultAgent != "reviewer" {
		t.Fatalf("Project.DefaultAgent = %q, want %q", cfg.Project.DefaultAgent, "reviewer")
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "reviewer" {
		t.Fatalf("Agents = %+v, want one agent named reviewer", cfg.Agents)
	}
	if len(cfg.Agents[0].Tools) != 3 {
		t.Fatalf("Agents[0].Tools = %v, want 3 entries", cfg.Agents[0].Tools)
	}
}

func TestMalformedConfigFailsLoudly(t *testing.T) {
	client := &stubGitClient{
		blobs: map[string][]byte{
			".novaforge/project.yaml": []byte("name: [this is not valid yaml"),
		},
	}

	cfg, err := repoconfig.Load(context.Background(), client, uuid.New(), uuid.New(), "main")
	if err == nil {
		t.Fatal("expected an error for malformed project.yaml")
	}
	if !strings.Contains(err.Error(), "novaforge config") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "novaforge config")
	}
	if !reflect.DeepEqual(cfg, repoconfig.Config{}) {
		t.Fatalf("Config = %+v, want the zero value on a malformed config", cfg)
	}
}

func TestAbsentConfigIsNotAnError(t *testing.T) {
	client := &stubGitClient{}

	cfg, err := repoconfig.Load(context.Background(), client, uuid.New(), uuid.New(), "main")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg, repoconfig.Config{}) {
		t.Fatalf("Config = %+v, want the zero value for a repository with no .novaforge directory", cfg)
	}
}

func TestUnknownAgentToolRejected(t *testing.T) {
	client := &stubGitClient{
		blobs: map[string][]byte{
			".novaforge/project.yaml": []byte("name: example\n"),
			".novaforge/agents/rogue.yaml": []byte(`
name: rogue
role: engineer
model: anthropic/claude-sonnet-5
tools:
  - shell.exec
`),
		},
		trees: map[string][]*gitv1.TreeEntry{
			".novaforge/agents": {
				{Kind: "blob", Name: "rogue.yaml"},
			},
		},
	}

	_, err := repoconfig.Load(context.Background(), client, uuid.New(), uuid.New(), "main")
	if err == nil {
		t.Fatal("expected an error for an agent naming an unregistered tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "unknown tool")
	}
}

func TestExactMCPToolSyntax(t *testing.T) {
	for _, name := range []string{"mcp.safe.lookup", "mcp.safe.repo.read", "mcp..lookup", "mcp.safe.", "mcp.safe.*", "mcp.safe..lookup", "work.gte"} {
		client := &stubGitClient{blobs: map[string][]byte{".novaforge/agents/test.yaml": []byte("name: test\nrole: engineer\ntools: [\"" + name + "\"]\n")}, trees: map[string][]*gitv1.TreeEntry{".novaforge/agents": {{Kind: "blob", Name: "test.yaml"}}}}
		_, err := repoconfig.Load(context.Background(), client, uuid.New(), uuid.New(), "main")
		valid := name == "mcp.safe.lookup" || name == "mcp.safe.repo.read"
		if (err == nil) != valid {
			t.Errorf("%s: valid=%v err=%v", name, valid, err)
		}
	}
}
