package reviews

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/work"
)

// The reviews service publishes inbox notifications for the observable
// actions it owns. The work schema is written only through work.Store's typed
// PublishInbox — never raw SQL across schemas — and the two packages are the
// two halves of one binary (work-reviews), which already pairs the stores
// (see cmd/work-reviews/automerge.go).
//
// A gate's proof RPC arrives under an org-scoped gates service token, and a
// review request under the requesting member's credential, so both carry the
// org their rows belong to and PublishInbox's admission admits them.

// notifyRunAuthor publishes one notification to a run's author, and is the
// single place that decides a run's author is the addressee: an agent's run
// has no inbox to reach (agents hold no mailbox on this platform — the
// publish is skipped rather than addressed to nobody), and a person who
// authored the run does.
func notifyRunAuthor(g *GRPCServer, ctx context.Context, run Run, reason, ref, title, body, dedupe string, actorID uuid.UUID, actorKind string) {
	if g.Inbox == nil {
		return
	}
	if run.AuthorKind != "user" || run.AuthorID == uuid.Nil {
		return
	}
	if _, err := g.Inbox.PublishInbox(ctx, run.OrgID, work.InboxItem{
		RepoID: run.RepoID, Reason: reason,
		Ref: ref, Title: title, Body: body,
		ActorID: actorID, ActorKind: actorKind,
		DedupeKey: dedupe,
	}, []uuid.UUID{run.AuthorID}); err != nil {
		// A notification is an observation about a succeeded action, never a
		// condition of it: a failed publish is logged and dropped rather than
		// failing the review request or the proof record that caused it.
		log.Printf("reviews: publish inbox notification: %v", err)
	}
}

// gateFailureDedupe keeps one failing gate at one recorded outcome to one
// notification: a re-evaluation that records the same proof again is not news.
func gateFailureDedupe(runID uuid.UUID, gate, detail string) string {
	sum := sha256.Sum256([]byte(gate + "\x00" + detail))
	return "gate:" + runID.String() + ":" + gate + ":" + hex.EncodeToString(sum[:8])
}
