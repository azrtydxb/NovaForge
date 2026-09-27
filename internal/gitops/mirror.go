package gitops

// Import and mirroring: bringing a repository in from another Git host, and
// keeping it following that host until the migration is finished.
//
// Two things here are load-bearing and neither is obvious.
//
// The credential. Git's own idiom for a private remote is
// https://user:token@host/path, and every part of this file exists to avoid it:
// a URL like that is stored in a table, printed in every error git emits, and
// visible in the process table of every fetch to anyone who can read it. The
// credential is stored as AES-GCM ciphertext under the service KEK, and is
// handed to git through the environment of the child process — never in argv,
// never on disk, never in the remote URL.
//
// A deployment caveat that is not a code problem and must not be discovered by
// a confused operator: git-platform is listed in the chart's
// networkPolicy.airGapped, so its egress is confined to the cluster. Importing
// from a host outside the cluster therefore fails at connect until git-platform
// is taken off that list, which is a deliberate security trade for whoever runs
// the platform to make — not something this package should quietly assume. An
// import from an in-cluster Git host works as it stands.
//
// The directory. An import clones into a throwaway directory beside the
// destination and renames it into place only once the clone has finished, so a
// clone that dies half way leaves neither a repository row nor a directory. A
// clone straight into the destination would leave a partial repository that is
// indistinguishable from an empty one, and re-importing the same name would then
// fail because the directory already exists.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/novaforge/novaforge/internal/egress"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// Errors a caller has to be able to tell apart.
var (
	// ErrMirror is returned for a push to a mirror. It is not a permission
	// problem and must not read like one: the pusher may well have every right to
	// write here, and the reason is that this repository's history is not the
	// platform's to change.
	ErrMirror = errors.New("this repository is a mirror and accepts no pushes")
	// ErrNoMirror is an absent mirror, including one held by another
	// organization — the two are deliberately indistinguishable.
	ErrNoMirror = errors.New("this repository is not a mirror")
	// ErrNoKEK is returned when a credential is offered to a store with no KEK.
	// The caller is told plainly rather than having the credential dropped or
	// stored in the clear, either of which looks like it worked.
	ErrNoKEK = errors.New("this deployment has no SECRETS_KEK, so a mirror credential cannot be stored")
)

// DefaultMirrorInterval is how often a mirror is refreshed when the caller did
// not say. It exists because 0 is what an unset protobuf field looks like, and
// "refresh on every pass" is a poor reading of somebody who said nothing.
const DefaultMirrorInterval = time.Hour

// mirrorUsername is the HTTP Basic username used with a mirror credential.
//
// Every Git host that authenticates with a token ignores the username (this
// platform's own transport does too — see NewCredentialAuthFunc), but the Basic
// scheme requires one, and git will otherwise prompt for it and hang.
const mirrorUsername = "novaforge"

// dueMirrorBatch bounds one pass of the mirrorer. A deployment with thousands of
// mirrors should take several passes rather than fork thousands of fetches.
const dueMirrorBatch = 200

// Mirror is what this platform knows about a repository's upstream. It
// deliberately has no credential field: the struct is what every read returns,
// so a credential it does not carry cannot leak through a listing, a log line or
// a JSON response somebody added later.
type Mirror struct {
	RepoID        uuid.UUID
	OrgID         uuid.UUID
	Remote        string
	Interval      time.Duration
	LastSyncedAt  *time.Time
	LastError     string
	HasCredential bool
}

// ImportRequest is one import. It is a struct rather than a parameter list
// because an import has two independent booleans' worth of intent — mirror or
// not, and on what schedule — and a positional credential next to a positional
// name is exactly the argument people swap.
//
// It carries no organization: the organization is taken from the caller's scope
// (see authz), never from a request field, or a caller could name another
// organization and be believed.
type ImportRequest struct {
	Name       string
	Remote     string
	Credential string
	// Mirror keeps following upstream after the import. Without it the import is
	// a one-off copy, the credential is not stored at all, and the repository is
	// writable like any other — which is what somebody migrating off the old host
	// for good wants.
	Mirror bool
	// Interval is the gap between refreshes. 0 means every pass of the mirrorer.
	Interval time.Duration
}

// Imported is the repository an import produced.
type Imported struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	Name          string
	DefaultBranch string
	Mirror        *Mirror
}

// MirrorStore owns imports and mirrors. It needs the git data root as well as
// the database because an import is mostly a clone, and a refresh is a fetch
// into a repository on disk.
type MirrorStore struct {
	Outbound *egress.Policy
	pool     *pgxpool.Pool
	root     string
	// key is sha256(KEK), the same derivation secrets.NewBroker and
	// webhooks.NewStore use: all three encrypt at rest under SECRETS_KEK, and
	// two derivations of one key would mean a value written by one is
	// undecryptable by the other.
	key [32]byte
	// kekSet distinguishes "no KEK configured" from a KEK that hashes to
	// something. Without it an unconfigured deployment would encrypt every
	// credential under sha256(""), which is not encryption at all.
	kekSet bool
}

// NewMirrorStore wraps pool. kek is the raw SECRETS_KEK value; an empty one
// leaves the store unable to hold a credential, and it refuses to store one
// rather than keeping it under a key everybody knows.
func NewMirrorStore(pool *pgxpool.Pool, root string, kek []byte) *MirrorStore {
	s := &MirrorStore{pool: pool, root: root}
	if len(kek) > 0 {
		s.key, s.kekSet = sha256.Sum256(kek), true
	}
	return s
}

// managing returns the organization a caller may import into and administer
// mirrors in, taken from the scope the server interceptor resolved.
//
// An agent is refused, and so is an outside collaborator (RequireOrg is what
// refuses a repository-limited scope). An import fetches arbitrary code from an
// arbitrary address using a credential the platform then stores; that is an
// organization-level act, and a grant of one repository is not a grant of it.
func (s *MirrorStore) managing(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if scope.OrgID == uuid.Nil {
		return uuid.Nil, errors.New("importing and mirroring require an organization scope")
	}
	if err := authz.RequireOrg(ctx, scope.OrgID); err != nil {
		return uuid.Nil, err
	}
	if scope.ActorKind != "user" {
		return uuid.Nil, fmt.Errorf("a %s may not import a repository or change its mirror", scope.ActorKind)
	}
	return scope.OrgID, nil
}

// ImportRepo clones remote into a new repository in the caller's organization.
//
// Nothing is committed until the clone has finished: on any failure there is no
// repository row and no directory, so the same import can simply be retried.
func (s *MirrorStore) ImportRepo(ctx context.Context, req ImportRequest) (Imported, error) {
	orgID, err := s.managing(ctx)
	if err != nil {
		return Imported{}, err
	}
	if !repoNameRe.MatchString(req.Name) {
		return Imported{}, fmt.Errorf("invalid repository name %q", req.Name)
	}
	if err := validateRemote(req.Remote); err != nil {
		return Imported{}, err
	}
	// Refuse before doing any work, rather than cloning for a minute and then
	// discovering the credential cannot be kept.
	if req.Mirror && req.Credential != "" && !s.kekSet {
		return Imported{}, ErrNoKEK
	}

	final, err := resolvePath(s.root, orgID, req.Name)
	if err != nil {
		return Imported{}, err
	}
	if _, err := os.Stat(final); err == nil {
		return Imported{}, fmt.Errorf("a repository directory already exists at %s", req.Name)
	}
	orgDir := filepath.Dir(final)
	if err := os.MkdirAll(orgDir, 0o700); err != nil {
		return Imported{}, fmt.Errorf("create organization directory: %w", err)
	}
	// The throwaway sits beside the destination so the rename is within one
	// filesystem and therefore atomic; a temporary directory elsewhere would make
	// os.Rename fail with EXDEV on any host where /tmp is its own mount. The
	// leading dot keeps it out of repoNameRe's range, so it can never collide
	// with a repository somebody could ask for by name.
	tmp := filepath.Join(orgDir, ".import-"+uuid.NewString()+".git")
	// Removed on every path. After a successful rename it is already gone and
	// this is a no-op, which is what makes "a failed import leaves no directory"
	// true without a second bookkeeping variable to get wrong.
	defer func() { _ = os.RemoveAll(tmp) }()

	if _, err := s.git(ctx, "", req.Credential, "clone", "--mirror", "--quiet", req.Remote, tmp); err != nil {
		return Imported{}, fmt.Errorf("clone %s: %w", req.Remote, err)
	}
	// The imported repository's own HEAD, not a database opinion about it: a
	// default branch of "main" recorded for a repository whose upstream uses
	// "master" would make the GUI ask for a branch that does not exist.
	branch := "main"
	if out, err := s.git(ctx, "", "", "--git-dir="+tmp, "symbolic-ref", "--short", "HEAD"); err == nil {
		if got := strings.TrimSpace(string(out)); got != "" {
			branch = got
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Imported{}, fmt.Errorf("begin import: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := uuid.New()
	if _, err := tx.Exec(ctx,
		`INSERT INTO gitplatform.repositories (id, org_id, name, default_branch)
		 VALUES ($1, $2, $3, $4)`,
		id, orgID, req.Name, branch); err != nil {
		var pgErr *pgconn.PgError
		if isUniqueViolation(err, &pgErr) {
			return Imported{}, fmt.Errorf("repository %q already exists", req.Name)
		}
		return Imported{}, fmt.Errorf("create repository record: %w", err)
	}

	var mirror *Mirror
	if req.Mirror {
		var credential []byte
		if req.Credential != "" {
			if credential, err = s.encrypt(req.Credential); err != nil {
				return Imported{}, err
			}
		}
		// last_synced_at is now: the clone just brought everything upstream had,
		// so counting the mirror as immediately due would fetch it all again.
		now := time.Now().UTC()
		if _, err := tx.Exec(ctx,
			`INSERT INTO gitplatform.repository_mirrors
			   (repo_id, org_id, remote, credential, interval_seconds, last_synced_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			id, orgID, req.Remote, credential, int32(req.Interval.Seconds()), now); err != nil {
			return Imported{}, fmt.Errorf("record the mirror: %w", err)
		}
		mirror = &Mirror{
			RepoID: id, OrgID: orgID, Remote: req.Remote, Interval: req.Interval,
			LastSyncedAt: &now, HasCredential: len(credential) > 0,
		}
	}

	// The directory move happens before the commit, so a failed move leaves the
	// transaction to roll back with nothing changed — the same order admin.go's
	// rename and transfer use, for the same reason.
	if err := os.Rename(tmp, final); err != nil {
		return Imported{}, fmt.Errorf("move the imported repository into place: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		// Put it back under the throwaway name so the deferred removal takes it:
		// a directory left at the destination with no row would make the same
		// import fail forever.
		_ = os.Rename(final, tmp)
		return Imported{}, fmt.Errorf("commit import: %w", err)
	}
	return Imported{ID: id, OrgID: orgID, Name: req.Name, DefaultBranch: branch, Mirror: mirror}, nil
}

// SetMirror points a repository at an upstream, or repoints an existing mirror.
// An empty credential removes the stored one, which is how a mirror of a
// repository that has been made public is deliberately made anonymous.
func (s *MirrorStore) SetMirror(ctx context.Context, repoID uuid.UUID, remote, credential string, interval time.Duration) error {
	orgID, err := s.managing(ctx)
	if err != nil {
		return err
	}
	if err := validateRemote(remote); err != nil {
		return err
	}
	if credential != "" && !s.kekSet {
		return ErrNoKEK
	}
	var blob []byte
	if credential != "" {
		if blob, err = s.encrypt(credential); err != nil {
			return err
		}
	}
	// The repository must be one of this organization's. The predicate is on the
	// repositories row rather than on the mirror row, because a mirror row may
	// not exist yet and an insert cannot check what is not there.
	var name string
	err = s.pool.QueryRow(ctx,
		`SELECT name FROM gitplatform.repositories WHERE id = $1 AND org_id = $2`,
		repoID, orgID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no repository %s in this organization", repoID)
	}
	if err != nil {
		return fmt.Errorf("resolve repository: %w", err)
	}
	// last_synced_at goes back to NULL, which makes the mirror due at once. What
	// was fetched before came from whatever the remote used to be, so claiming it
	// as this remote's last successful sync would be a lie that delays the first
	// real fetch by a whole interval.
	_, err = s.pool.Exec(ctx, `
		INSERT INTO gitplatform.repository_mirrors
		  (repo_id, org_id, remote, credential, interval_seconds, last_synced_at, last_error)
		VALUES ($1, $2, $3, $4, $5, NULL, '')
		ON CONFLICT (repo_id) DO UPDATE SET
		  remote = EXCLUDED.remote,
		  credential = EXCLUDED.credential,
		  interval_seconds = EXCLUDED.interval_seconds,
		  last_synced_at = NULL,
		  last_error = ''`,
		repoID, orgID, remote, blob, int32(interval.Seconds()))
	if err != nil {
		return fmt.Errorf("set the mirror: %w", err)
	}
	return nil
}

// GetMirror returns what is known about a repository's upstream. The credential
// is not part of it.
func (s *MirrorStore) GetMirror(ctx context.Context, repoID uuid.UUID) (Mirror, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Mirror{}, err
	}
	if err := authz.RequireRepo(ctx, scope.OrgID, repoID); err != nil {
		return Mirror{}, err
	}
	m, _, _, err := s.mirror(ctx, scope.OrgID, repoID)
	return m, err
}

// DeleteMirror stops a repository following upstream, which also makes it
// writable again: the push guard reads exactly this row.
func (s *MirrorStore) DeleteMirror(ctx context.Context, repoID uuid.UUID) error {
	orgID, err := s.managing(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM gitplatform.repository_mirrors WHERE repo_id = $1 AND org_id = $2`,
		repoID, orgID)
	if err != nil {
		return fmt.Errorf("delete the mirror: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoMirror
	}
	return nil
}

// mirror reads one mirror row with an organization predicate, returning the
// public view, the repository's name and the decrypted credential. The
// credential is returned separately from Mirror so it cannot be carried out of
// this package by accident.
func (s *MirrorStore) mirror(ctx context.Context, orgID, repoID uuid.UUID) (Mirror, string, string, error) {
	var (
		m        Mirror
		seconds  int32
		blob     []byte
		repoName string
	)
	// A join within one schema, which is what "no cross-schema reads" permits:
	// repositories and repository_mirrors are both git-platform's own.
	err := s.pool.QueryRow(ctx, `
		SELECT m.remote, m.credential, m.interval_seconds, m.last_synced_at, m.last_error, r.name
		FROM gitplatform.repository_mirrors m
		JOIN gitplatform.repositories r ON r.id = m.repo_id AND r.org_id = m.org_id
		WHERE m.repo_id = $1 AND m.org_id = $2`,
		repoID, orgID).Scan(&m.Remote, &blob, &seconds, &m.LastSyncedAt, &m.LastError, &repoName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mirror{}, "", "", ErrNoMirror
	}
	if err != nil {
		return Mirror{}, "", "", fmt.Errorf("read the mirror: %w", err)
	}
	m.RepoID, m.OrgID = repoID, orgID
	m.Interval = time.Duration(seconds) * time.Second
	m.HasCredential = len(blob) > 0
	var credential string
	if len(blob) > 0 {
		if credential, err = s.decrypt(blob); err != nil {
			// A credential that cannot be decrypted (a rotated KEK, a corrupted
			// row) is reported as such rather than treated as absent: fetching
			// anonymously instead would fail against a private upstream with a
			// message about authentication that sends nobody to the real cause.
			return Mirror{}, "", "", fmt.Errorf("mirror credential for %s: %w", repoName, err)
		}
	}
	return m, repoName, credential, nil
}

// Refresh fetches upstream into one mirrored repository and records the outcome.
//
// It returns the fetch's error as well as recording it, so a caller running one
// mirror on demand learns what happened; the mirrorer logs it and carries on to
// the next mirror, because one unreachable upstream must not stop every other.
func (s *MirrorStore) Refresh(ctx context.Context, repoID uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	m, repoName, credential, err := s.mirror(ctx, scope.OrgID, repoID)
	if err != nil {
		return err
	}
	path, err := resolvePath(s.root, scope.OrgID, repoName)
	if err != nil {
		return err
	}

	// The refspecs are given explicitly rather than relying on the clone's own
	// remote.origin configuration, because SetMirror can repoint a mirror and the
	// config would still name the address it was cloned from. --force is what
	// makes this a mirror and not a merge: upstream owns these refs, so a
	// rewritten branch upstream must become a rewritten branch here.
	_, fetchErr := s.git(ctx, "", credential, "--git-dir="+path, "fetch", "--quiet", "--prune", "--force",
		m.Remote, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	if fetchErr != nil {
		reason := scrub(fetchErr.Error(), credential)
		if _, err := s.pool.Exec(ctx,
			`UPDATE gitplatform.repository_mirrors SET last_error = $1 WHERE repo_id = $2 AND org_id = $3`,
			reason, repoID, scope.OrgID); err != nil {
			return fmt.Errorf("record the failed refresh: %w", err)
		}
		return errors.New(reason)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE gitplatform.repository_mirrors
		 SET last_synced_at = now(), last_error = '' WHERE repo_id = $1 AND org_id = $2`,
		repoID, scope.OrgID); err != nil {
		return fmt.Errorf("record the refresh: %w", err)
	}
	return nil
}

// mirrorRef is one due mirror, as ids and nothing else.
type mirrorRef struct {
	OrgID  uuid.UUID
	RepoID uuid.UUID
}

// dueMirrors returns the mirrors whose interval has elapsed.
//
// It is a platform-worker query: it crosses every organization and carries no
// org predicate, which nothing else in this package is allowed to do. It returns
// only ids, and its single caller re-enters each organization's scope before
// reading or writing anything for it — the same shape as work.Store.OpenEpics
// and agents.Store.staleRunning.
func (s *MirrorStore) dueMirrors(ctx context.Context) ([]mirrorRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, repo_id FROM gitplatform.repository_mirrors
		WHERE last_synced_at IS NULL
		   OR last_synced_at + (interval_seconds * interval '1 second') <= now()
		ORDER BY last_synced_at NULLS FIRST
		LIMIT $1`, dueMirrorBatch)
	if err != nil {
		return nil, fmt.Errorf("list due mirrors: %w", err)
	}
	defer rows.Close()
	var refs []mirrorRef
	for rows.Next() {
		var r mirrorRef
		if err := rows.Scan(&r.OrgID, &r.RepoID); err != nil {
			return nil, fmt.Errorf("scan due mirror: %w", err)
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due mirrors: %w", err)
	}
	return refs, nil
}

// Mirrorer keeps every mirror following its upstream. It is the whole feature:
// without something calling Run, an imported repository silently never changes
// again, which looks exactly like an upstream nobody has pushed to.
type Mirrorer struct {
	Store *MirrorStore
	// Tick is the gap between passes. Each mirror's own interval decides whether
	// it is due on a given pass, so this only bounds how promptly a due mirror is
	// noticed.
	Tick time.Duration
	// Logf reports a refresh that failed. A failing mirror must be visible
	// somewhere other than a column nobody reads; it defaults to the standard
	// logger.
	Logf func(format string, args ...any)
}

// Run refreshes due mirrors until ctx is cancelled.
func (m *Mirrorer) Run(ctx context.Context) error {
	tick := m.Tick
	if tick <= 0 {
		tick = time.Minute
	}
	timer := time.NewTicker(tick)
	defer timer.Stop()
	for {
		if _, err := m.RunOnce(ctx); err != nil {
			// A failure here is the query for due mirrors, not one mirror's
			// fetch, so it is almost certainly the database; log and keep the
			// loop alive rather than taking the worker down with it.
			m.logf("git-platform: mirrorer pass failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// RunOnce refreshes every mirror that is due and returns how many succeeded.
// Its error is reserved for a failure to find out what is due at all; one
// mirror's unreachable upstream is logged and recorded, and the pass continues.
func (m *Mirrorer) RunOnce(ctx context.Context) (int, error) {
	refs, err := m.Store.dueMirrors(ctx)
	if err != nil {
		return 0, err
	}
	refreshed := 0
	for _, ref := range refs {
		// Back inside one organization's scope before anything of that
		// organization is read or written. The scope is a platform service
		// acting in that organization, which is what the webhook delivery
		// worker does for the same reason.
		scoped := authz.WithScope(ctx, authz.Scope{OrgID: ref.OrgID, ActorKind: "service"})
		if err := m.Store.Refresh(scoped, ref.RepoID); err != nil {
			// The message is already scrubbed by Refresh; repo id and
			// organization are identifiers, not secrets.
			m.logf("git-platform: mirror refresh failed for repository %s in organization %s: %v",
				ref.RepoID, ref.OrgID, err)
			continue
		}
		refreshed++
	}
	return refreshed, nil
}

func (m *Mirrorer) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// NewMirrorCapFunc refuses a push to a mirror, over either transport.
//
// It is a CapFunc so the smart-HTTP handler and the SSH server get it from the
// one place they already share, rather than each growing its own check that
// could drift — the same reasoning as newArchiveGuard, and it lives in this
// package rather than in cmd/git-platform so a test can run the function the
// cluster actually runs. CapFunc is called with the ref updates for
// git-receive-pack and with none for git-upload-pack, so cloning a mirror is
// unaffected: a mirror exists to be read.
func NewMirrorCapFunc(pool *pgxpool.Pool) CapFunc {
	return func(ctx context.Context, _ authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		if len(refs) == 0 {
			return nil
		}
		// The transports address a repository by name; resolve by either so the
		// guard holds if a caller ever presents an id.
		column, value := "name", any(repo)
		if id, err := uuid.Parse(repo); err == nil {
			column, value = "id", any(id)
		}
		var remote string
		err := pool.QueryRow(ctx, `
			SELECT m.remote
			FROM gitplatform.repository_mirrors m
			JOIN gitplatform.repositories r ON r.id = m.repo_id AND r.org_id = m.org_id
			WHERE m.org_id = $1 AND r.`+column+` = $2`, orgID, value).Scan(&remote)
		if errors.Is(err, pgx.ErrNoRows) {
			// Not a mirror, or not a repository at all. A missing repository is
			// the transport's own lookup to report; answering here would turn it
			// into a permission error.
			return nil
		}
		if err != nil {
			return fmt.Errorf("check whether %s is a mirror: %w", repo, err)
		}
		// The refusal names upstream, because the useful next action is to push
		// there. "permission denied" would send somebody looking for a grant
		// that is not the reason.
		return fmt.Errorf("%w: %s owns this history — push there instead", ErrMirror, remote)
	}
}

// validateRemote accepts the addresses this platform is willing to fetch from.
//
// http and https only. An ssh:// or scp-style remote would need a private key
// this platform does not hold, and would fall back to whatever key the pod
// happens to have — the service's own. file:// and a bare local path are refused
// outright: they would let a caller clone any directory the git-platform process
// can read, which includes every other organization's repositories.
func validateRemote(remote string) error {
	if remote == "" {
		return errors.New("a remote URL is required")
	}
	if strings.HasPrefix(remote, "-") {
		// A remote beginning with a dash is read by git as an option.
		return fmt.Errorf("invalid remote %q", remote)
	}
	u, err := url.Parse(remote)
	if err != nil {
		return fmt.Errorf("invalid remote %q", remote)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("remote %q must be an http or https URL", remote)
	}
	if u.Host == "" {
		return fmt.Errorf("remote %q names no host", remote)
	}
	if u.User != nil {
		// Refused rather than quietly stripped. Somebody who pasted a URL with a
		// token in it needs to know the token was in it, so they can revoke it —
		// and it has already been written to whatever log recorded the request.
		return errors.New("the remote URL must not contain a username or password; supply the credential separately")
	}
	return nil
}

// git runs the git binary with the mirror credential supplied through the
// child's environment.
//
// The credential reaches git through GIT_ASKPASS, an executable git calls when a
// server asks for Basic credentials. It is the only channel that keeps the
// credential out of argv (readable by any process on the host), out of the
// remote URL (which ends up in tables, configs and error messages) and off disk
// (the helper script it writes contains no secret — only the two environment
// lookups). GIT_TERMINAL_PROMPT=0 is what turns a missing or wrong credential
// into an error instead of a fetch that hangs forever waiting for a terminal
// nobody is attached to.
func (s *MirrorStore) git(ctx context.Context, dir, credential string, args ...string) ([]byte, error) {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if s.Outbound != nil {
		for _, arg := range args {
			if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
				config, err := s.Outbound.GitConfig(ctx, arg)
				if err != nil {
					return nil, err
				}
				args = append(config, args...)
				break
			}
		}
		env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "http_proxy=", "https_proxy=", "all_proxy=")
	}
	if credential != "" {
		helper, cleanup, err := writeAskpass()
		if err != nil {
			return nil, err
		}
		defer cleanup()
		env = append(env,
			"GIT_ASKPASS="+helper,
			"NF_GIT_MIRROR_USERNAME="+mirrorUsername,
			"NF_GIT_MIRROR_PASSWORD="+credential)
	}

	// An empty credential.helper resets the configured list, so no helper of the
	// host's — a keychain, a cached store, a pod's mounted gitconfig — is
	// consulted. Without this, git asks the helpers first and only calls
	// GIT_ASKPASS if they all decline: a helper holding a stale credential for
	// the same host answers instead, the fetch fails as unauthenticated, and the
	// credential this platform was given is never even offered. It was caught
	// exactly that way — a mirror test passed alone and failed after another test
	// in the package had made git cache a credential for 127.0.0.1.
	args = append([]string{"-c", "credential.helper="}, args...)

	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// git's own output is scrubbed as well as the arguments. Nothing here is
		// supposed to contain the credential — that is the point of the askpass
		// helper — and scrubbing anyway is what keeps a future change that
		// reintroduces it from turning into a leak instead of a test failure.
		return nil, fmt.Errorf("git %s: %w — %s",
			scrub(strings.Join(args, " "), credential), err, scrub(stderr.String(), credential))
	}
	return []byte(stdout.String()), nil
}

// askpassScript answers git's Username and Password prompts from the
// environment. It holds no secret itself, which is why it is safe to write to
// disk at all.
const askpassScript = `#!/bin/sh
case "$1" in
Username*) printf '%s' "$NF_GIT_MIRROR_USERNAME" ;;
*) printf '%s' "$NF_GIT_MIRROR_PASSWORD" ;;
esac
`

func writeAskpass() (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "nf-askpass-")
	if err != nil {
		return "", nil, fmt.Errorf("create askpass directory: %w", err)
	}
	path = filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(path, []byte(askpassScript), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("write askpass helper: %w", err)
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

// scrub removes secrets from a message that is about to be stored or logged: the
// literal credential, and any userinfo in a URL, since a remote somebody
// configured elsewhere may carry one even though this package refuses to.
func scrub(msg string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "«redacted»")
		}
	}
	return scrubURLUserinfo(msg)
}

// scrubURLUserinfo rewrites scheme://user:pass@host to scheme://«redacted»@host
// anywhere in msg.
func scrubURLUserinfo(msg string) string {
	var b strings.Builder
	rest := msg
	for {
		i := strings.Index(rest, "://")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+3])
		rest = rest[i+3:]
		// The userinfo, if any, ends at the first '@' before the authority ends.
		end := strings.IndexAny(rest, "/ \t\n\"'")
		authority := rest
		if end >= 0 {
			authority = rest[:end]
		}
		at := strings.LastIndex(authority, "@")
		if at < 0 {
			continue
		}
		b.WriteString("«redacted»")
		rest = rest[at:]
	}
}

func (s *MirrorStore) encrypt(plaintext string) ([]byte, error) {
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
	// silently returning garbage that would then be sent to somebody's Git host.
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (s *MirrorStore) decrypt(blob []byte) (string, error) {
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
		return "", errors.New("cannot be decrypted under this deployment's KEK")
	}
	return string(plaintext), nil
}
