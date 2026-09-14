package reviews_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// TestCreateRunAttributesThePersonCalling pins who a person-opened run is
// attributed to. The author came from the request: the edge sent none, so
// every run opened through the API had no author, and any caller could have
// named someone else — or claimed to be an agent — and been believed.
func TestCreateRunAttributesThePersonCalling(t *testing.T) {
	srv := newGRPCServer(t)
	orgID, person := uuid.New(), uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorID: person, ActorKind: "user"})

	resp, err := srv.CreateRun(ctx, &reviewsv1.CreateRunRequest{
		RepoId:     uuid.NewString(),
		Title:      "tidy the parser",
		SourceRef:  "feature/parser",
		TargetRef:  "main",
		AuthorId:   uuid.NewString(),
		AuthorKind: "agent",
		AgentName:  "impostor",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	run := resp.GetRun()
	if run.GetAuthorId() != person.String() || run.GetAuthorKind() != "user" {
		t.Fatalf("author = %s (%s), want the calling person %s (user)", run.GetAuthorId(), run.GetAuthorKind(), person)
	}
	if run.GetAgentName() != "" {
		t.Fatalf("a person's run carries agent name %q", run.GetAgentName())
	}
}

// TestCreateRunRefusesAnIncompleteRun pins that a run which cannot be
// reviewed or merged is refused when it is opened, not discovered later: a
// run with no title, no branch to merge, or a branch merging into itself.
func TestCreateRunRefusesAnIncompleteRun(t *testing.T) {
	srv := newGRPCServer(t)
	ctx := scopedCtx(uuid.New())
	repo := uuid.NewString()

	for name, req := range map[string]*reviewsv1.CreateRunRequest{
		"no title":         {RepoId: repo, SourceRef: "feature", TargetRef: "main", AuthorKind: "user"},
		"no source":        {RepoId: repo, Title: "t", TargetRef: "main", AuthorKind: "user"},
		"no target":        {RepoId: repo, Title: "t", SourceRef: "feature", AuthorKind: "user"},
		"source is target": {RepoId: repo, Title: "t", SourceRef: "main", TargetRef: "refs/heads/main", AuthorKind: "user"},
	} {
		_, err := srv.CreateRun(ctx, req)
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", name, status.Code(err))
		}
	}
}
