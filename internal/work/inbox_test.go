package work_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/work"
)

// The inbox is per-recipient mail. A member of another organization is never
// shown a notification that is not theirs, a cross-org credential cannot read
// or transition another org's rows, and every transition checks the caller is
// the row's recipient — so one organization's member cannot quietly dismiss
// another organization's notification even when they know its id.

func scopedCtxAs(actor, orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID: orgID, ActorID: actor, ActorKind: "user",
	})
}

func platformCtx() context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		ActorKind: "service", PlatformWorker: "gates",
	})
}

func TestInboxPublishAndListPerRecipient(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	alice := uuid.New()
	bob := uuid.New()

	pub := scopedCtxAs(alice, orgID)
	item := work.InboxItem{
		RepoID: repoID, Reason: work.ReasonReviewRequested,
		Ref: "run #1", Title: "Inline review threads on diffs",
		Body: "+412 -38 in 11 files", DedupeKey: "agent-review:1",
	}

	if _, err := store.PublishInbox(pub, orgID, item, []uuid.UUID{bob}); err != nil {
		t.Fatalf("PublishInbox: %v", err)
	}

	// Re-publishing with the same dedupe key adds nothing: the observable
	// event happened once, and a retry is not news.
	again, err := store.PublishInbox(pub, orgID, item, []uuid.UUID{bob})
	if err != nil {
		t.Fatalf("PublishInbox again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("want dedupe to insert nothing, got %d rows", len(again))
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want int
	}{
		{"recipient sees it", scopedCtxAs(bob, orgID), 1},
		{"a member of another org sees none", scopedCtxAs(uuid.New(), uuid.New()), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, err := store.ListInbox(tc.ctx, work.InboxFilter{State: work.InboxStateInbox})
			if err != nil {
				t.Fatalf("ListInbox: %v", err)
			}
			if len(items) != tc.want {
				t.Fatalf("want %d notifications, got %d", tc.want, len(items))
			}
		})
	}
}

func TestInboxUnreadCountsOnlyLiveInboxRows(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	me := uuid.New()

	ctx := scopedCtxAs(me, orgID)
	item := work.InboxItem{
		RepoID: uuid.New(), Reason: work.ReasonGateFailure,
		Ref: "run #2", Title: "coverage-gate: 71.4% (needs 75%)",
		Body: "edge/api lost 3.8 points.", DedupeKey: "gate:1",
	}
	if _, err := store.PublishInbox(ctx, orgID, item, []uuid.UUID{me}); err != nil {
		t.Fatalf("PublishInbox: %v", err)
	}

	items, err := store.ListInbox(ctx, work.InboxFilter{State: work.InboxStateInbox})
	if err != nil {
		t.Fatalf("ListInbox: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 notification before transitions, got %d", len(items))
	}
	n := items[0]

	// A snoozed row leaves both the unread count and the inbox list, then
	// returns by itself once its snooze has passed.
	if err := store.InboxSnooze(ctx, n.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("InboxSnooze: %v", err)
	}
	if got, err := store.InboxUnread(ctx); err != nil || got != 0 {
		t.Fatalf("want 0 unread while snoozed, got %d (%v)", got, err)
	}
	if items, err := store.ListInbox(ctx, work.InboxFilter{State: work.InboxStateInbox}); err != nil || len(items) != 0 {
		t.Fatalf("want empty inbox while snoozed, got %d (%v)", len(items), err)
	}
	if err := store.InboxSnooze(ctx, n.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("InboxSnooze past: %v", err)
	}
	if got, err := store.InboxUnread(ctx); err != nil || got != 1 {
		t.Fatalf("want 1 unread after the snooze passed, got %d (%v)", got, err)
	}

	// Done removes it from unread and from the inbox list for good — and the
	// design's Done tab still reads it back under state=done.
	if err := store.InboxDone(ctx, n.ID); err != nil {
		t.Fatalf("InboxDone: %v", err)
	}
	if got, err := store.InboxUnread(ctx); err != nil || got != 0 {
		t.Fatalf("want 0 unread after done, got %d (%v)", got, err)
	}
	done, err := store.ListInbox(ctx, work.InboxFilter{State: work.InboxStateDone})
	if err != nil {
		t.Fatalf("ListInbox done: %v", err)
	}
	if len(done) != 1 || done[0].ID != n.ID {
		t.Fatalf("want the done row under state=done, got %d rows", len(done))
	}
}

func TestInboxSaveRoundTrip(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	me := uuid.New()
	ctx := scopedCtxAs(me, orgID)
	item := work.InboxItem{
		RepoID: uuid.New(), Reason: work.ReasonApproval,
		Ref: "run #3", Title: "Deploy novaforge 0.9.2 to production",
		DedupeKey: "approval:1",
	}
	if _, err := store.PublishInbox(ctx, orgID, item, []uuid.UUID{me}); err != nil {
		t.Fatalf("PublishInbox: %v", err)
	}
	items, err := store.ListInbox(ctx, work.InboxFilter{State: work.InboxStateInbox})
	if err != nil {
		t.Fatalf("ListInbox: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 notification, got %d", len(items))
	}
	n := items[0]

	// Saved is its own tab: the row leaves unread and inbox, returns to the
	// inbox when unsaved, and stays the recipient's own decision throughout.
	if err := store.InboxSave(ctx, n.ID, true); err != nil {
		t.Fatalf("InboxSave true: %v", err)
	}
	saved, err := store.ListInbox(ctx, work.InboxFilter{State: work.InboxStateSaved})
	if err != nil || len(saved) != 1 || saved[0].ID != n.ID {
		t.Fatalf("want the row under state=saved, got %d rows (%v)", len(saved), err)
	}
	if got, err := store.InboxUnread(ctx); err != nil || got != 0 {
		t.Fatalf("want 0 unread while saved, got %d (%v)", got, err)
	}
	if err := store.InboxSave(ctx, n.ID, false); err != nil {
		t.Fatalf("InboxSave false: %v", err)
	}
	back, err := store.ListInbox(ctx, work.InboxFilter{State: work.InboxStateInbox})
	if err != nil || len(back) != 1 || back[0].ID != n.ID {
		t.Fatalf("want the row back in the inbox, got %d rows (%v)", len(back), err)
	}
}

func TestInboxPublishAuthorization(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()

	// A user of another organization may not publish into this one, even
	// when they can name it in a request field.
	if _, err := store.PublishInbox(scopedCtxAs(uuid.New(), uuid.New()), orgID, work.InboxItem{
		RepoID: uuid.New(), Reason: work.ReasonMaintenance,
		Ref: "maint-1", Title: "Quarantine flaky test",
	}, []uuid.UUID{uuid.New()}); err == nil {
		t.Fatal("want cross-org publish refused")
	}

	// A platform worker names no organization of its own and publishes on
	// the platform's behalf — the same admission the maintenance and review
	// workers hold everywhere else.
	if _, err := store.PublishInbox(platformCtx(), orgID, work.InboxItem{
		RepoID: uuid.New(), Reason: work.ReasonGateFailure,
		Ref: "run #9", Title: "secrets-gate failed",
	}, []uuid.UUID{uuid.New()}); err != nil {
		t.Fatalf("platform worker publish: %v", err)
	}
}

func TestInboxTransitionsAreTheRecipientsOwn(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	me := uuid.New()
	other := uuid.New()
	ctx := scopedCtxAs(me, orgID)

	if _, err := store.PublishInbox(ctx, orgID, work.InboxItem{
		RepoID: uuid.New(), Reason: work.ReasonReviewRequested,
		Ref: "run #4", Title: "t",
	}, []uuid.UUID{me}); err != nil {
		t.Fatalf("PublishInbox: %v", err)
	}
	items, err := store.ListInbox(ctx, work.InboxFilter{State: work.InboxStateInbox})
	if err != nil || len(items) != 1 {
		t.Fatalf("want 1 notification, got %d (%v)", len(items), err)
	}
	n := items[0]

	// A co-member of the same organization who is not the recipient cannot
	// dismiss someone else's notification, with or without its id.
	for _, act := range []struct {
		name string
		call func() error
	}{
		{"done", func() error { return store.InboxDone(scopedCtxAs(other, orgID), n.ID) }},
		{"snooze", func() error { return store.InboxSnooze(scopedCtxAs(other, orgID), n.ID, time.Now().Add(time.Hour)) }},
		{"save", func() error { return store.InboxSave(scopedCtxAs(other, orgID), n.ID, true) }},
	} {
		if err := act.call(); err == nil {
			t.Fatalf("%s by a non-recipient was allowed", act.name)
		}
	}
	if items, err := store.ListInbox(ctx, work.InboxFilter{State: work.InboxStateInbox}); err != nil || len(items) != 1 {
		t.Fatalf("the row must survive a refused transition, got %d (%v)", len(items), err)
	}
}

func TestInboxListFilters(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	me := uuid.New()
	repoA := uuid.New()
	repoB := uuid.New()
	ctx := scopedCtxAs(me, orgID)

	for _, item := range []work.InboxItem{
		{RepoID: repoA, Reason: work.ReasonGateFailure, Ref: "run #1", Title: "f"},
		{RepoID: repoB, Reason: work.ReasonApproval, Ref: "run #2", Title: "a"},
	} {
		if _, err := store.PublishInbox(ctx, orgID, item, []uuid.UUID{me}); err != nil {
			t.Fatalf("PublishInbox: %v", err)
		}
	}

	byReason, err := store.ListInbox(ctx, work.InboxFilter{Reason: work.ReasonApproval})
	if err != nil || len(byReason) != 1 || byReason[0].Reason != work.ReasonApproval {
		t.Fatalf("want 1 approval row, got %d (%v)", len(byReason), err)
	}
	byRepo, err := store.ListInbox(ctx, work.InboxFilter{RepoID: repoA})
	if err != nil || len(byRepo) != 1 || byRepo[0].RepoID != repoA {
		t.Fatalf("want 1 row for repo A, got %d (%v)", len(byRepo), err)
	}
	all, err := store.ListInbox(ctx, work.InboxFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("want 2 rows unfiltered, got %d (%v)", len(all), err)
	}
}
