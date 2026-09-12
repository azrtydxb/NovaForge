package work_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/work"
)

func TestAddAndListComments(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	item, err := store.Create(ctx, work.Item{
		OrgID: orgID, RepoID: uuid.New(), Type: "feature", Goal: "commented on",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.AddComment(ctx, item.ID, "first"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	// An agent's comment is attributed as an agent's, so a reader can tell
	// which of the two wrote a line.
	agentCtx := authz.WithScope(context.Background(), authz.Scope{
		OrgID: orgID, ActorID: uuid.New(), ActorKind: "agent",
	})
	if _, err := store.AddComment(agentCtx, item.ID, "second, from an agent"); err != nil {
		t.Fatalf("AddComment as an agent: %v", err)
	}

	comments, err := store.ListComments(ctx, item.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("want 2 comments, got %d", len(comments))
	}
	if comments[0].Body != "first" || comments[0].AuthorKind != "user" {
		t.Fatalf("first comment is wrong: %+v", comments[0])
	}
	if comments[1].AuthorKind != "agent" {
		t.Fatalf("want the second comment attributed to an agent, got %q", comments[1].AuthorKind)
	}
}

// TestCommentsAreOrgScoped pins that a Work Item in another organization
// cannot be commented on, and that its thread cannot be read: organizations
// are a hard boundary, and a comment RPC is a way across it if it is not
// scoped like every other read.
func TestCommentsAreOrgScoped(t *testing.T) {
	store := newStore(t)
	orgA, orgB := uuid.New(), uuid.New()

	item, err := store.Create(scopedCtx(orgA), work.Item{
		OrgID: orgA, RepoID: uuid.New(), Type: "feature", Goal: "org A's item",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.AddComment(scopedCtx(orgA), item.ID, "visible to org A"); err != nil {
		t.Fatalf("AddComment in org A: %v", err)
	}

	if _, err := store.AddComment(scopedCtx(orgB), item.ID, "should not be possible"); err == nil {
		t.Fatal("org B was able to comment on org A's Work Item")
	}
	got, err := store.ListComments(scopedCtx(orgB), item.ID)
	if err != nil {
		t.Fatalf("ListComments for org B: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("org B read %d of org A's comments", len(got))
	}
}

func TestAddCommentRejectsAnEmptyBody(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	item, err := store.Create(ctx, work.Item{
		OrgID: orgID, RepoID: uuid.New(), Type: "feature", Goal: "x",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.AddComment(ctx, item.ID, ""); err == nil {
		t.Fatal("want an error for an empty comment body")
	}
}
