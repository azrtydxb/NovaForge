package work

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
)

// Reasons the design's REASON filters name. Every one is written by a
// publisher that observed the action; a reason with no publisher simply never
// appears, which is the honest rendering — the GUI lists the filters statically
// and an empty filter is an empty filter.
const (
	ReasonReviewRequested = "review_requested"
	ReasonApproval        = "approval"
	ReasonGateFailure     = "gate_failure"
	ReasonMaintenance     = "maintenance"
	ReasonMention         = "mention"
	ReasonAgentQuestion   = "agent_question"
)

// validReasons is the set the schema's CHECK constraint admits. PublishInbox
// refuses a reason outside it so a typo fails at the publisher, not as a row
// the database rejects half-inserted.
var validReasons = map[string]bool{
	ReasonReviewRequested: true,
	ReasonApproval:        true,
	ReasonGateFailure:     true,
	ReasonMaintenance:     true,
	ReasonMention:         true,
	ReasonAgentQuestion:   true,
}

// Inbox states. "inbox" is the live list the design's Inbox tab reads; "saved"
// is the Saved tab; "done" is the Done tab.
const (
	InboxStateInbox = "inbox"
	InboxStateSaved = "saved"
	InboxStateDone  = "done"
)

// ErrInboxNotFound is returned by every inbox transition whose row is not
// visible to the caller: wrong id, another user's row, or another
// organization's. The three are indistinguishable on purpose.
var ErrInboxNotFound = errors.New("notification not found")

// InboxItem is one notification addressed to one recipient.
type InboxItem struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	RecipientID  uuid.UUID
	RepoID       uuid.UUID
	Reason       string
	Ref          string
	Title        string
	Body         string
	ActorID      uuid.UUID
	ActorKind    string
	ActorName    string
	DedupeKey    string
	State        string
	SnoozedUntil *time.Time
	CreatedAt    time.Time
}

// InboxFilter selects a slice of one recipient's inbox.
type InboxFilter struct {
	State  string
	Reason string
	RepoID uuid.UUID
	Limit  int
}

// inboxColumns is the column list every inbox read scans back.
const inboxColumns = `id, org_id, recipient_id, repo_id, reason, ref, title, body,
	actor_id, actor_kind, actor_name, dedupe_key, state, snoozed_until, created_at`

func scanInboxItem(row pgx.Row) (InboxItem, error) {
	var n InboxItem
	err := row.Scan(&n.ID, &n.OrgID, &n.RecipientID, &n.RepoID, &n.Reason, &n.Ref,
		&n.Title, &n.Body, &n.ActorID, &n.ActorKind, &n.ActorName, &n.DedupeKey,
		&n.State, &n.SnoozedUntil, &n.CreatedAt)
	return n, err
}

// authorizePublish admits two callers and nobody else: a member acting inside
// the organization they name, or a platform worker publishing on the
// platform's behalf. Anyone else — a member of another organization, a
// repository-limited collaborator — is refused, so a caller cannot publish a
// notification into an org it can name but does not belong to.
func authorizePublish(ctx context.Context, orgID uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if scope.IsPlatformWorker() {
		return nil
	}
	return authz.RequireOrg(ctx, orgID)
}

// PublishInbox inserts one row per recipient and returns the rows it actually
// created — empty when every row hit its dedupe key, so a caller can tell
// "first time" from "again". The org is a parameter because the platform's
// workers (the gate controller recording proof, the review worker) publish on
// runs they observe from outside any organization; every other caller must be
// acting inside the organization it names. The recipient list is the
// publisher's own decision, taken from what it observed — who authored the
// run, who raised the request — never from a request field.
func (s *Store) PublishInbox(ctx context.Context, orgID uuid.UUID, n InboxItem, recipients []uuid.UUID) ([]InboxItem, error) {
	if err := authorizePublish(ctx, orgID); err != nil {
		return nil, err
	}
	if !validReasons[n.Reason] {
		return nil, fmt.Errorf("invalid inbox reason %q", n.Reason)
	}
	if n.Ref == "" || n.Title == "" {
		return nil, fmt.Errorf("a notification needs a ref and a title")
	}
	if n.State == "" {
		n.State = InboxStateInbox
	}

	var created []InboxItem
	for _, r := range recipients {
		if r == uuid.Nil {
			continue
		}
		n.RecipientID = r
		err := s.pool.QueryRow(ctx, `
			INSERT INTO work.inbox_notifications
				(org_id, recipient_id, repo_id, reason, ref, title, body,
				 actor_id, actor_kind, actor_name, dedupe_key, state)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT DO NOTHING
			RETURNING `+inboxColumns,
			orgID, r, n.RepoID, n.Reason, n.Ref, n.Title, n.Body,
			n.ActorID, n.ActorKind, n.ActorName, n.DedupeKey, n.State,
		).Scan(&n.ID, &n.OrgID, &n.RecipientID, &n.RepoID, &n.Reason, &n.Ref,
			&n.Title, &n.Body, &n.ActorID, &n.ActorKind, &n.ActorName, &n.DedupeKey,
			&n.State, &n.SnoozedUntil, &n.CreatedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue // deduped: this event already reached this recipient
			}
			return nil, fmt.Errorf("publish inbox notification: %w", err)
		}
		created = append(created, n)
	}
	return created, nil
}

// ListInbox reads one recipient's notifications. The org and the recipient
// both come from the caller's scope — never from a request field — so a
// member of another organization is not even addressable here, and a
// platform worker (which names no org and no recipient) reads nothing.
func (s *Store) ListInbox(ctx context.Context, f InboxFilter) ([]InboxItem, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	if scope.OrgID == uuid.Nil || scope.ActorID == uuid.Nil || scope.ActorKind == "service" {
		return nil, fmt.Errorf("an inbox is one user's mail in one organization")
	}
	if f.State == "" {
		f.State = InboxStateInbox
	}
	if f.Limit == 0 {
		f.Limit = 200
	}
	var repoArg *uuid.UUID
	if f.RepoID != uuid.Nil {
		repoArg = &f.RepoID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+inboxColumns+` FROM work.inbox_notifications
		WHERE org_id = $1 AND recipient_id = $2 AND state = $3
		  AND reason = COALESCE(NULLIF($4, ''), reason)
		  AND ($5::uuid IS NULL OR repo_id = $5::uuid)
		  -- a snoozed row returns by itself; until then it is not in the list
		  AND (state <> 'inbox' OR snoozed_until IS NULL OR snoozed_until <= now())
		ORDER BY created_at DESC
		LIMIT $6`,
		scope.OrgID, scope.ActorID, f.State, f.Reason, repoArg, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("list inbox: %w", err)
	}
	defer rows.Close()
	var out []InboxItem
	for rows.Next() {
		n, err := scanInboxItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan inbox notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// InboxUnread counts the caller's live notifications. Unlike every other
// inbox read this deliberately carries no organization predicate, and is one
// of the documented exceptions to the boundary rule: the recipient predicate
// is the caller's own id from their credential, the rows returned are the
// caller's own mail and nothing of anyone else's, and the count spans the
// caller's organizations because the design's header badge is a single
// number — "Inbox · 7 unread" — that cannot know which org a row came from.
func (s *Store) InboxUnread(ctx context.Context) (int, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return 0, err
	}
	if scope.ActorID == uuid.Nil || scope.ActorKind == "service" {
		return 0, fmt.Errorf("an inbox count needs a user")
	}
	var n int
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM work.inbox_notifications
		WHERE recipient_id = $1 AND state = 'inbox'
		  AND (snoozed_until IS NULL OR snoozed_until <= now())`,
		scope.ActorID,
	).Scan(&n)
	return n, err
}

// inboxTransition is the one UPDATE every inbox mutation goes through, so the
// org, recipient and (for a live row) unsnoozed predicates cannot drift apart
// between done, snooze and save. Rows affected 0 means the caller named a row
// that is not theirs to touch, and every caller reports that as not found.
func (s *Store) inboxTransition(ctx context.Context, id uuid.UUID, set string, args ...any) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if scope.OrgID == uuid.Nil || scope.ActorID == uuid.Nil || scope.ActorKind == "service" {
		return fmt.Errorf("an inbox is one user's mail in one organization")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE work.inbox_notifications SET `+set+`
		WHERE id = $1 AND org_id = $2 AND recipient_id = $3`,
		append([]any{id, scope.OrgID, scope.ActorID}, args...)...)
	if err != nil {
		return fmt.Errorf("inbox transition: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInboxNotFound
	}
	return nil
}

// InboxDone marks one of the caller's own notifications done.
func (s *Store) InboxDone(ctx context.Context, id uuid.UUID) error {
	return s.inboxTransition(ctx, id, `state = 'done', snoozed_until = NULL`)
}

// InboxSnooze hides one of the caller's own notifications until until, after
// which it returns to their inbox by itself.
func (s *Store) InboxSnooze(ctx context.Context, id uuid.UUID, until time.Time) error {
	return s.inboxTransition(ctx, id, `snoozed_until = $4`, until)
}

// InboxSave moves one of the caller's own notifications between the Saved tab
// and the inbox. Saving keeps a row out of unread: a thing the recipient has
// deliberately set aside is not a thing shouting for attention.
func (s *Store) InboxSave(ctx context.Context, id uuid.UUID, saved bool) error {
	if saved {
		return s.inboxTransition(ctx, id, `state = 'saved', snoozed_until = NULL`)
	}
	return s.inboxTransition(ctx, id, `state = 'inbox', snoozed_until = NULL`)
}
