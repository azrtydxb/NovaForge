package gates_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/gates"
)

// stubGitClient implements gitv1.GitServiceClient by embedding the nil
// interface (any method not overridden below panics if called, which is
// deliberate: Resolve must never call anything but GetTree/GetBlob) and
// answering GetTree/GetBlob per-ref from in-memory fixtures.
type stubGitClient struct {
	gitv1.GitServiceClient
	// tree maps ref -> entries under .novaforge/gates
	tree map[string][]*gitv1.TreeEntry
	// blobs maps "ref|path" -> file content
	blobs map[string][]byte
}

func (s *stubGitClient) GetTree(_ context.Context, in *gitv1.GetTreeRequest, _ ...grpc.CallOption) (*gitv1.GetTreeResponse, error) {
	return &gitv1.GetTreeResponse{Entries: s.tree[in.Ref]}, nil
}

func (s *stubGitClient) GetBlob(_ context.Context, in *gitv1.GetBlobRequest, _ ...grpc.CallOption) (*gitv1.GetBlobResponse, error) {
	content, ok := s.blobs[in.Ref+"|"+in.Path]
	if !ok {
		return nil, errNotFound(in.Path)
	}
	return &gitv1.GetBlobResponse{Content: content}, nil
}

func errNotFound(path string) error { return status.Errorf(codes.NotFound, "not found: %s", path) }

func TestResolveReadsFromTargetRefNotSource(t *testing.T) {
	client := &stubGitClient{
		tree: map[string][]*gitv1.TreeEntry{
			"main": {
				{Kind: "blob", Name: "tests.yaml"},
				{Kind: "blob", Name: "security.yaml"},
			},
			"agents/work/x": {},
		},
		blobs: map[string][]byte{
			"main|.novaforge/gates/tests.yaml":    []byte("name: tests\nrequired: true\n"),
			"main|.novaforge/gates/security.yaml": []byte("name: security\nrequired: true\n"),
		},
	}

	defs, err := gates.Resolve(context.Background(), client, uuid.New(), uuid.New(), "main", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("want 2 definitions from target ref, got %d: %+v", len(defs), defs)
	}
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["tests"] || !names["security"] {
		t.Fatalf("want tests and security resolved from target ref, got %+v", defs)
	}
}

func TestWorkItemGatesAreUnioned(t *testing.T) {
	client := &stubGitClient{
		tree: map[string][]*gitv1.TreeEntry{
			"main": {
				{Kind: "blob", Name: "tests.yaml"},
			},
		},
		blobs: map[string][]byte{
			"main|.novaforge/gates/tests.yaml": []byte("name: tests\nrequired: true\n"),
		},
	}

	defs, err := gates.Resolve(context.Background(), client, uuid.New(), uuid.New(), "main", []string{"api-compatibility"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var found bool
	for _, d := range defs {
		if d.Name == "api-compatibility" {
			found = true
			if !d.Required {
				t.Fatal("want work item gate to be required")
			}
		}
	}
	if !found {
		t.Fatalf("want api-compatibility unioned in from work item gates, got %+v", defs)
	}
}

func TestUnknownGateNameRejected(t *testing.T) {
	client := &stubGitClient{
		tree: map[string][]*gitv1.TreeEntry{
			"main": {
				{Kind: "blob", Name: "nonsense.yaml"},
			},
		},
		blobs: map[string][]byte{
			"main|.novaforge/gates/nonsense.yaml": []byte("name: nonsense\nrequired: true\n"),
		},
	}

	_, err := gates.Resolve(context.Background(), client, uuid.New(), uuid.New(), "main", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown gate") {
		t.Fatalf("want error containing 'unknown gate', got %v", err)
	}
}

func TestMalformedGateConfigFailsClosed(t *testing.T) {
	client := &stubGitClient{
		tree: map[string][]*gitv1.TreeEntry{
			"main": {
				{Kind: "blob", Name: "tests.yaml"},
			},
		},
		blobs: map[string][]byte{
			"main|.novaforge/gates/tests.yaml": []byte("name: [this is not valid: yaml"),
		},
	}

	defs, err := gates.Resolve(context.Background(), client, uuid.New(), uuid.New(), "main", nil)
	if err == nil {
		t.Fatal("want error for malformed gate config")
	}
	if len(defs) != 0 {
		t.Fatalf("want no definitions on malformed config, got %+v", defs)
	}
}
