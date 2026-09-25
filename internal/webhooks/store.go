// Package webhooks notifies an endpoint outside the platform when something
// happens in a repository, and keeps the record of what that endpoint answered.
//
// It is a consumer of the streams the platform already publishes, not a new
// event system: a push is delivered because git-platform published it, so a hook
// can never see something that did not happen.
//
// The hook's shared secret is write-only. It is stored as AES-GCM ciphertext
// under the service KEK, exactly as secrets.secret_values holds a stored
// credential, and no method on this package returns it — the only thing that
// reads it back is signing a delivery.
package webhooks

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// maxHooksPerRepo bounds how many endpoints one repository can fan a push out
// to. A push already does real work; an unbounded list of endpoints would let a
// single repository turn every push into an arbitrary amount of outbound traffic.
const maxHooksPerRepo = 20

// Hook is a registered endpoint. It deliberately has no secret field: the
// struct is what every read returns, so a secret it does not carry cannot leak
// through a listing, a log line, or a JSON response somebody added later.
type Hook struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	RepoID    uuid.UUID `json:"repo_id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	HasSecret bool      `json:"has_secret"`
	CreatedAt time.Time `json:"created_at"`
}

// Delivery is one attempt to notify one endpoint.
type Delivery struct {
	ID         uuid.UUID `json:"id"`
	HookID     uuid.UUID `json:"hook_id"`
	Event      string    `json:"event"`
	StatusCode int       `json:"status_code"`
	Error      string    `json:"error"`
	Attempt    int       `json:"attempt"`
	At         time.Time `json:"at"`
}

// Delivered reports whether the endpoint accepted this attempt. It is the same
// judgement the worker makes, so the GUI cannot disagree with the retry logic
// about what "succeeded" means.
func (d Delivery) Delivered() bool {
	return d.StatusCode >= 200 && d.StatusCode < 300
}

// Store owns the hooks and their delivery history.
type Store struct {
	pool *pgxpool.Pool
	// key is sha256(KEK), so an operator is not forced to pick an exact
	// 16/24/32-byte value — the same derivation secrets.NewBroker uses, because
	// both are encrypting at rest under SECRETS_KEK and two derivations of one
	// key would mean a value written by one is undecryptable by the other.
	key [32]byte
	// kekSet distinguishes "no KEK configured" from a KEK that hashes to
	// something. Without it an unconfigured deployment would encrypt every
	// secret under sha256("") — which is not a secret at all.
	kekSet bool
}

// NewStore wraps pool. kek is the raw SECRETS_KEK value; an empty one leaves the
// store unable to hold a secret, and it refuses to store one rather than keeping
// it under a key everybody knows.
func NewStore(pool *pgxpool.Pool, kek []byte) *Store {
	s := &Store{pool: pool}
	if len(kek) > 0 {
		s.key, s.kekSet = sha256.Sum256(kek), true
	}
	return s
}

// ErrNoKEK is returned when a secret is offered to a store with no KEK. The
// caller is told plainly rather than having the secret dropped or stored in the
// clear, either of which looks like it worked.
var ErrNoKEK = errors.New("this deployment has no SECRETS_KEK, so a webhook secret cannot be stored")

// managing returns the organization a caller may administer hooks in, taken from
// the scope the server interceptor resolved and never from an argument.
//
// RequireOrg is called with the scope's own organization, which looks like a
// tautology and is not: it is what refuses a repository-limited scope (an outside
// collaborator). A webhook is standing permission to send everything a push
// carries to an address off the platform, and a grant of one repository is not a
// grant of that.
//
// An agent is refused for the same reason: no capability grant covers egress, so
// an agent that talked its way into a credential still cannot register somewhere
// to send the code it can read.
func (s *Store) managing(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if scope.OrgID == uuid.Nil {
		return uuid.Nil, errors.New("webhook administration requires an organization scope")
	}
	if err := authz.RequireOrg(ctx, scope.OrgID); err != nil {
		return uuid.Nil, err
	}
	if scope.ActorKind != "user" {
		return uuid.Nil, fmt.Errorf("a %s may not administer webhooks", scope.ActorKind)
	}
	return scope.OrgID, nil
}

// reading returns the organization a caller may read hooks in. The worker reads
// with a service scope for the organization whose push it is delivering, which
// administration refuses, so the two are separate functions rather than one with
// a flag nobody would notice was set.
func (s *Store) reading(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if scope.OrgID == uuid.Nil {
		return uuid.Nil, errors.New("reading webhooks requires an organization scope")
	}
	if err := authz.RequireOrg(ctx, scope.OrgID); err != nil {
		return uuid.Nil, err
	}
	return scope.OrgID, nil
}

// ValidateURL refuses an endpoint the platform should not be made to call.
//
// Only http and https, because the delivery is an HTTP POST and any other scheme
// means the URL was never going to work. A host is required: "https:///hook" is
// accepted by url.Parse and would have the worker POST to nothing forever.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhook url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook url scheme %q is not http or https", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("webhook url has no host")
	}
	return nil
}

// CreateHook registers an endpoint for a repository. secret may be empty, which
// means deliveries to it are unsigned; events may be empty, which means every
// event.
func (s *Store) CreateHook(ctx context.Context, repoID uuid.UUID, endpoint string, events []string, secret string) (Hook, error) {
	orgID, err := s.managing(ctx)
	if err != nil {
		return Hook{}, err
	}
	if err := ValidateURL(endpoint); err != nil {
		return Hook{}, err
	}
	events = normalizeEvents(events)

	var ciphertext []byte
	if secret != "" {
		if ciphertext, err = s.encrypt(secret); err != nil {
			return Hook{}, err
		}
	}

	hook := Hook{
		ID: uuid.New(), OrgID: orgID, RepoID: repoID, URL: endpoint,
		Events: events, Active: true, HasSecret: len(ciphertext) > 0,
	}
	// The repository is matched on its organization too, so a caller cannot
	// register a hook on another organization's repository by naming its id —
	// the same shape the collaborator grants use, for the same reason.
	//
	// The count is taken in the same statement as the insert so two concurrent
	// requests cannot both see themselves as the twentieth hook.
	err = s.pool.QueryRow(ctx, `
		INSERT INTO gitplatform.hooks (id, org_id, repo_id, url, events, secret)
		SELECT $1, $2, r.id, $4, $5, $6
		  FROM gitplatform.repositories r
		 WHERE r.id = $3 AND r.org_id = $2
		   AND (SELECT count(*) FROM gitplatform.hooks h WHERE h.repo_id = r.id) < $7
		RETURNING created_at`,
		hook.ID, orgID, repoID, endpoint, events, ciphertext, maxHooksPerRepo,
	).Scan(&hook.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing was inserted: either the repository is not this organization's,
		// or the limit is reached. Which one it is matters to the caller.
		var count int
		if err := s.pool.QueryRow(ctx,
			`SELECT count(*) FROM gitplatform.hooks h
			   JOIN gitplatform.repositories r ON r.id = h.repo_id
			  WHERE h.repo_id = $1 AND r.org_id = $2`, repoID, orgID).Scan(&count); err == nil && count >= maxHooksPerRepo {
			return Hook{}, fmt.Errorf("this repository already has the maximum of %d webhooks", maxHooksPerRepo)
		}
		return Hook{}, fmt.Errorf("no repository %s in this organization", repoID)
	}
	if err != nil {
		return Hook{}, fmt.Errorf("create hook: %w", err)
	}
	return hook, nil
}

// normalizeEvents trims and drops empties so a hook asking for "" (which nothing
// publishes) is not silently subscribed to nothing while looking filtered.
func normalizeEvents(events []string) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// ListHooks returns a repository's hooks, without their secrets.
func (s *Store) ListHooks(ctx context.Context, repoID uuid.UUID) ([]Hook, error) {
	orgID, err := s.reading(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, repo_id, url, events, active, secret IS NOT NULL, created_at
		  FROM gitplatform.hooks
		 WHERE org_id = $1 AND repo_id = $2
		 ORDER BY created_at`, orgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("list hooks: %w", err)
	}
	defer rows.Close()
	out := []Hook{}
	for rows.Next() {
		var h Hook
		if err := rows.Scan(&h.ID, &h.OrgID, &h.RepoID, &h.URL, &h.Events, &h.Active, &h.HasSecret, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan hook: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHook returns one hook, without its secret.
func (s *Store) GetHook(ctx context.Context, id uuid.UUID) (Hook, error) {
	orgID, err := s.reading(ctx)
	if err != nil {
		return Hook{}, err
	}
	var h Hook
	err = s.pool.QueryRow(ctx, `
		SELECT id, org_id, repo_id, url, events, active, secret IS NOT NULL, created_at
		  FROM gitplatform.hooks WHERE id = $1 AND org_id = $2`, id, orgID,
	).Scan(&h.ID, &h.OrgID, &h.RepoID, &h.URL, &h.Events, &h.Active, &h.HasSecret, &h.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Hook{}, fmt.Errorf("no webhook %s in this organization", id)
	}
	if err != nil {
		return Hook{}, fmt.Errorf("get hook: %w", err)
	}
	return h, nil
}

// DeleteHook removes a hook and, by cascade, its delivery history.
func (s *Store) DeleteHook(ctx context.Context, id uuid.UUID) error {
	orgID, err := s.managing(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM gitplatform.hooks WHERE id = $1 AND org_id = $2`, id, orgID)
	if err != nil {
		return fmt.Errorf("delete hook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no webhook %s in this organization", id)
	}
	return nil
}

// SetSecret sets or rotates a hook's secret. An empty secret removes it, which is
// how a hook is deliberately made unsigned again; there is no read that returns
// either the old value or the new one.
func (s *Store) SetSecret(ctx context.Context, id uuid.UUID, secret string) error {
	orgID, err := s.managing(ctx)
	if err != nil {
		return err
	}
	var ciphertext []byte
	if secret != "" {
		if ciphertext, err = s.encrypt(secret); err != nil {
			return err
		}
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE gitplatform.hooks SET secret = $3 WHERE id = $1 AND org_id = $2`, id, orgID, ciphertext)
	if err != nil {
		// The error is built from ids only. A wrapped driver error here has been
		// known to carry the statement's parameters, and one of them is the
		// secret.
		return fmt.Errorf("rotate secret of webhook %s", id)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no webhook %s in this organization", id)
	}
	return nil
}

// SetActive turns a hook on or off without losing it or its history — the honest
// way to stop a noisy endpoint while whoever owns it fixes it.
func (s *Store) SetActive(ctx context.Context, id uuid.UUID, active bool) error {
	orgID, err := s.managing(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE gitplatform.hooks SET active = $3 WHERE id = $1 AND org_id = $2`, id, orgID, active)
	if err != nil {
		return fmt.Errorf("set hook active: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no webhook %s in this organization", id)
	}
	return nil
}

// ListDeliveries returns a hook's attempts, newest first.
func (s *Store) ListDeliveries(ctx context.Context, hookID uuid.UUID, limit int) ([]Delivery, error) {
	orgID, err := s.reading(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// The hook is resolved in this scope first, so a caller who names a hook of
	// another organization is told there is no such hook rather than being handed
	// an empty history — which reads as "this endpoint has never been called" and
	// is a different, and false, statement.
	if _, err := s.GetHook(ctx, hookID); err != nil {
		return nil, err
	}
	// The join is the organization predicate: hook_deliveries carries no org_id
	// of its own, so reading it by hook id alone would answer across the
	// boundary for anyone who learned a hook's id.
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.hook_id, d.event, d.status_code, d.error, d.attempt, d.at
		  FROM gitplatform.hook_deliveries d
		  JOIN gitplatform.hooks h ON h.id = d.hook_id
		 WHERE d.hook_id = $1 AND h.org_id = $2
		 ORDER BY d.at DESC, d.attempt DESC
		 LIMIT $3`, hookID, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.HookID, &d.Event, &d.StatusCode, &d.Error, &d.Attempt, &d.At); err != nil {
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// target is a hook plus the secret to sign its delivery with. It is unexported
// and returned only to the worker: this is the one path that decrypts a secret,
// and keeping it off Hook is what makes "no read returns the secret" a property
// of the type rather than a rule every future reader has to remember.
type target struct {
	hook   Hook
	secret string
}

// targets returns the active hooks of one repository that asked for event.
//
// The organization comes from ctx, which the worker scopes to the organization
// named by the event it is handling — an event published by git-platform itself,
// never a field from a client request. This is the same re-entry the indexer
// makes for a push (see internal/indexing).
func (s *Store) targets(ctx context.Context, repoID uuid.UUID, event string) ([]target, error) {
	orgID, err := s.reading(ctx)
	if err != nil {
		return nil, err
	}
	// The event filter is applied in SQL so a repository with many hooks does not
	// decrypt a secret it is not about to use: cardinality(events) = 0 is the
	// "every event" case.
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, repo_id, url, events, active, secret IS NOT NULL, created_at, secret
		  FROM gitplatform.hooks
		 WHERE org_id = $1 AND repo_id = $2 AND active
		   AND (cardinality(events) = 0 OR $3 = ANY(events))
		 ORDER BY created_at`, orgID, repoID, event)
	if err != nil {
		return nil, fmt.Errorf("hooks for %s: %w", event, err)
	}
	defer rows.Close()
	var out []target
	for rows.Next() {
		var t target
		var ciphertext []byte
		if err := rows.Scan(&t.hook.ID, &t.hook.OrgID, &t.hook.RepoID, &t.hook.URL, &t.hook.Events,
			&t.hook.Active, &t.hook.HasSecret, &t.hook.CreatedAt, &ciphertext); err != nil {
			return nil, fmt.Errorf("scan hook: %w", err)
		}
		if len(ciphertext) > 0 {
			// A secret that cannot be decrypted (a rotated KEK, a corrupted row)
			// must not silently become an unsigned delivery: the receiver checks
			// the signature, and one that is absent looks like a forgery. So the
			// hook is skipped and the reason recorded as a failed delivery.
			secret, err := s.decrypt(ciphertext)
			if err != nil {
				s.recordDelivery(ctx, Delivery{
					HookID: t.hook.ID, Event: event, Attempt: 1,
					Error: "the hook's secret could not be decrypted, so nothing was sent unsigned",
				})
				continue
			}
			t.secret = secret
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// recordDelivery appends one attempt. A failure to record is logged by the
// caller rather than aborting delivery: the notification has already been sent,
// and pretending otherwise would have the worker send it again.
func (s *Store) recordDelivery(ctx context.Context, d Delivery) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	if d.At.IsZero() {
		d.At = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO gitplatform.hook_deliveries (id, hook_id, event, status_code, error, attempt, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.ID, d.HookID, d.Event, d.StatusCode, d.Error, d.Attempt, d.At)
	if err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	return nil
}

func (s *Store) encrypt(plaintext string) ([]byte, error) {
	if !s.kekSet {
		return nil, ErrNoKEK
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// The nonce is prefixed to the ciphertext so it travels with it; GCM
	// authenticates both, so a tampered blob fails to decrypt rather than
	// silently returning garbage that would be signed with.
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (s *Store) decrypt(blob []byte) (string, error) {
	if !s.kekSet {
		return "", ErrNoKEK
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}
	if len(blob) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// The error deliberately says nothing about the value.
		return "", errors.New("decrypt webhook secret")
	}
	return string(plaintext), nil
}
