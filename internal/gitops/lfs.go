package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// Git LFS, served over the same transport and the same credential path as git
// itself.
//
// The whole feature is one rule: an LFS object is part of a repository, so
// reaching one must require exactly what reaching the repository requires. LFS
// is a separate HTTP API with its own endpoints, which is precisely how it
// becomes a way around repository access in other implementations — a batch
// endpoint that only checked organization membership would hand an outside
// collaborator, or an agent held to one branch, every binary in the
// organization. So the batch and transfer endpoints hang off httpHandler and go
// through the same AuthFunc and the same CapFunc as git-upload-pack and
// git-receive-pack, and there is no second authenticator anywhere in this file.

// DefaultLFSMaxObjectBytes bounds a single LFS object when the deployment names
// no limit.
//
// The default lives here rather than in service.LoadConfig on purpose: a
// non-zero default in LoadConfig makes internal/service's chart test treat
// NF_LFS_MAX_OBJECT_BYTES as optional, and the variable could then disappear
// from the pod without anything noticing — which is this repository's
// second-commonest defect. LoadConfig therefore reads it with no default, and
// the safety net is here.
const DefaultLFSMaxObjectBytes = 5 << 30

// Errors an LFS caller has to be able to tell apart.
var (
	// ErrLFSObjectNotFound is an absent object, including one held by another
	// organization or by another repository — all three are deliberately
	// indistinguishable, because an oid is a content hash and therefore known to
	// anyone who has the file.
	ErrLFSObjectNotFound = errors.New("no such LFS object")
	// ErrLFSObjectTooLarge is an object over the deployment's per-object limit.
	ErrLFSObjectTooLarge = errors.New("LFS object is larger than this deployment allows")
	// ErrLFSOIDMismatch means the bytes received do not hash to the oid they
	// were uploaded under. The object is not kept: an LFS oid is the only name a
	// client will ever ask for the content by, so content filed under the wrong
	// one is unreachable garbage at best and a substituted payload at worst.
	ErrLFSOIDMismatch = errors.New("the uploaded bytes do not hash to the object id")
)

// lfsOIDRe is a bare lower-case sha256 in hex — the spelling the batch API uses.
// The "sha256:" prefix belongs to the pointer file and never appears on the
// wire, and the oid becomes part of an object-storage key, so anything else is
// refused rather than sanitized.
var lfsOIDRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// LFSStore holds LFS objects: their bytes in the blobstore, one row per
// repository that has them.
type LFSStore struct {
	pool           *pgxpool.Pool
	blobs          BlobStore
	maxObjectBytes int64
}

// NewLFSStore builds the store. maxObjectBytes at or below zero means the
// deployment named no limit and DefaultLFSMaxObjectBytes applies — unbounded is
// not an option, because a single unbounded upload is how one repository fills
// the deployment's object storage for everyone.
func NewLFSStore(pool *pgxpool.Pool, blobs BlobStore, maxObjectBytes int64) *LFSStore {
	if maxObjectBytes <= 0 {
		maxObjectBytes = DefaultLFSMaxObjectBytes
	}
	return &LFSStore{pool: pool, blobs: blobs, maxObjectBytes: maxObjectBytes}
}

// MaxObjectBytes is the per-object limit in force.
func (s *LFSStore) MaxObjectBytes() int64 { return s.maxObjectBytes }

// lfsKey is the object key for one LFS object.
//
// The organization and the repository come first, ahead of the only part a
// caller chooses. Keying on the oid alone — the obvious shape, since the content
// is what the oid names, and the shape that would deduplicate storage across the
// whole deployment — makes every repository's copy the same object: one
// organization's push would be served to another organization that guessed the
// hash, and a repository deletion would delete bytes another organization is
// still using. Deduplication is not worth either.
func lfsKey(orgID, repoID uuid.UUID, oid string) string {
	return fmt.Sprintf("org/%s/repo/%s/lfs/%s", orgID, repoID, oid)
}

// LFSObject reports the recorded size of one object in a repository, and whether
// the repository has it at all.
//
// repoID must already have been resolved inside the caller's organization (see
// RepoForLFS, which is the only thing that resolves one here): the lookup is by
// primary key, so it carries no organization predicate of its own and would
// otherwise answer for any repository whose id a caller could name.
func (s *LFSStore) LFSObject(ctx context.Context, repoID uuid.UUID, oid string) (int64, bool, error) {
	if !lfsOIDRe.MatchString(oid) {
		return 0, false, fmt.Errorf("invalid LFS object id %q", oid)
	}
	var size int64
	err := s.pool.QueryRow(ctx,
		`SELECT size FROM gitplatform.lfs_objects WHERE repo_id = $1 AND oid = $2`,
		repoID, oid).Scan(&size)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("lookup LFS object: %w", err)
	}
	return size, true, nil
}

// RepoForLFS resolves a repository from its name or its id within orgID, and
// returns its default branch alongside.
//
// The organization comes from the caller's scope, never from the request, so
// naming another organization's repository — by name or by id — finds nothing.
// The default branch comes back because an LFS upload has to be authorized as a
// write against some ref, and a client that declared none needs a real one.
func (s *LFSStore) RepoForLFS(ctx context.Context, orgID uuid.UUID, ref string) (uuid.UUID, string, error) {
	if ref == "" {
		return uuid.Nil, "", errors.New("repo is required")
	}
	column, value := "name", any(ref)
	if id, err := uuid.Parse(ref); err == nil {
		column, value = "id", any(id)
	}
	var id uuid.UUID
	var branch string
	err := s.pool.QueryRow(ctx,
		`SELECT id, default_branch FROM gitplatform.repositories WHERE org_id = $1 AND `+column+` = $2`,
		orgID, value).Scan(&id, &branch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", fmt.Errorf("repository %q: %w", ref, ErrLFSObjectNotFound)
		}
		return uuid.Nil, "", fmt.Errorf("lookup repository: %w", err)
	}
	return id, branch, nil
}

// PutObject stores one object's bytes and records it against the repository.
//
// declared is the length the client announced, and is checked against the limit
// before a single byte is read — the criterion is that an oversized upload is
// refused before anything is stored. It is not trusted afterwards: the stream is
// also capped, so a client that understated its Content-Length, or sent none at
// all, still cannot exceed the limit.
func (s *LFSStore) PutObject(ctx context.Context, orgID, repoID uuid.UUID, oid string, r io.Reader, declared int64) (int64, error) {
	if s.blobs == nil {
		return 0, errors.New("this deployment has no object storage configured, so LFS objects cannot be stored")
	}
	if !lfsOIDRe.MatchString(oid) {
		return 0, fmt.Errorf("invalid LFS object id %q", oid)
	}
	if declared > s.maxObjectBytes {
		return 0, fmt.Errorf("%d bytes exceeds the %d byte limit: %w", declared, s.maxObjectBytes, ErrLFSObjectTooLarge)
	}

	key := lfsKey(orgID, repoID, oid)
	// Hashing on the way past is the only option: the body is a stream that
	// cannot be rewound, and buffering it would put a multi-gigabyte binary in
	// this process's memory once per concurrent upload.
	guard := &lfsGuardReader{r: r, limit: s.maxObjectBytes, hash: sha256.New()}
	if err := s.blobs.Put(ctx, key, guard, declared, "application/octet-stream"); err != nil {
		// Whatever was written is unreferenced and would never be found again,
		// so it goes before the failure is reported.
		_ = s.blobs.Delete(ctx, key)
		if errors.Is(err, ErrLFSObjectTooLarge) || errors.Is(guard.err, ErrLFSObjectTooLarge) {
			return 0, fmt.Errorf("the upload exceeded the %d byte limit: %w", s.maxObjectBytes, ErrLFSObjectTooLarge)
		}
		return 0, fmt.Errorf("store LFS object: %w", err)
	}
	if got := hex.EncodeToString(guard.hash.Sum(nil)); got != oid {
		_ = s.blobs.Delete(ctx, key)
		return 0, fmt.Errorf("received %s under %s: %w", got, oid, ErrLFSOIDMismatch)
	}

	// ON CONFLICT DO NOTHING because a client re-uploading an object the
	// repository already has is normal (a second branch, a second clone) and is
	// not an error: the content is identical by definition of the oid.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO gitplatform.lfs_objects (org_id, repo_id, oid, size)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (repo_id, oid) DO NOTHING`,
		orgID, repoID, oid, guard.n); err != nil {
		// The row is what makes the object reachable; an object with no row is
		// unreachable and would grow the bucket forever.
		if delErr := s.blobs.Delete(ctx, key); delErr != nil {
			return 0, fmt.Errorf("record LFS object: %w (and its object %s could not be removed: %v)", err, key, delErr)
		}
		return 0, fmt.Errorf("record LFS object: %w", err)
	}
	return guard.n, nil
}

// OpenObject opens one object for reading. It returns a reader rather than bytes
// for the same reason a release asset does: an LFS payload is routinely hundreds
// of megabytes, and buffering it would put that in memory per concurrent clone.
func (s *LFSStore) OpenObject(ctx context.Context, orgID, repoID uuid.UUID, oid string) (io.ReadCloser, int64, error) {
	if s.blobs == nil {
		return nil, 0, errors.New("this deployment has no object storage configured, so LFS objects cannot be served")
	}
	if !lfsOIDRe.MatchString(oid) {
		return nil, 0, fmt.Errorf("invalid LFS object id %q", oid)
	}
	var size int64
	// The organization predicate is applied here as well as on the repository
	// lookup that produced repoID. It is redundant by construction and stays
	// anyway: this is the query that hands out bytes, and it should be readable
	// on its own as scoped.
	err := s.pool.QueryRow(ctx,
		`SELECT size FROM gitplatform.lfs_objects WHERE org_id = $1 AND repo_id = $2 AND oid = $3`,
		orgID, repoID, oid).Scan(&size)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, fmt.Errorf("%s: %w", oid, ErrLFSObjectNotFound)
		}
		return nil, 0, fmt.Errorf("lookup LFS object: %w", err)
	}
	rc, err := s.blobs.Get(ctx, lfsKey(orgID, repoID, oid))
	if err != nil {
		return nil, 0, fmt.Errorf("open LFS object: %w", err)
	}
	return rc, size, nil
}

// lfsGuardReader hashes and counts what passes through it, and refuses to pass
// more than limit bytes. The cap is the half a declared Content-Length cannot
// give: a chunked upload declares nothing at all.
type lfsGuardReader struct {
	r     io.Reader
	limit int64
	n     int64
	hash  interface {
		io.Writer
		Sum([]byte) []byte
	}
	err error
}

func (g *lfsGuardReader) Read(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	n, err := g.r.Read(p)
	if n > 0 {
		g.n += int64(n)
		if g.n > g.limit {
			g.err = ErrLFSObjectTooLarge
			return 0, g.err
		}
		if _, werr := g.hash.Write(p[:n]); werr != nil {
			g.err = werr
			return 0, werr
		}
	}
	return n, err
}

// --- the batch API and object transfer, over the git transport ---

const (
	// lfsMediaType is the media type the batch API speaks, both ways. A client
	// that gets anything else treats the endpoint as not being an LFS server.
	lfsMediaType = "application/vnd.git-lfs+json"
	// lfsBatchPath and lfsObjectsPath are relative to {org}/{repo}.git/, which
	// is the endpoint git-lfs derives from the remote URL on its own: nothing
	// has to be configured in a clone for this to be found.
	lfsBatchPath   = "info/lfs/objects/batch"
	lfsObjectsPath = "info/lfs/objects/"
	// lfsMaxBatchBody bounds the request document. The batch body is a list of
	// pointers, never content, so a large one is a mistake or an attack.
	lfsMaxBatchBody = 1 << 20
)

type lfsPointer struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type lfsRef struct {
	Name string `json:"name"`
}

type lfsBatchRequest struct {
	Operation string       `json:"operation"`
	Transfers []string     `json:"transfers"`
	Ref       *lfsRef      `json:"ref"`
	Objects   []lfsPointer `json:"objects"`
	HashAlgo  string       `json:"hash_algo"`
}

type lfsAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header,omitempty"`
}

type lfsError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lfsObjectResponse struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
	// Authenticated tells the client the href it was handed already carries
	// everything needed to use it, so it does not go back to the credential
	// helper between the batch call and the transfer.
	Authenticated bool                 `json:"authenticated,omitempty"`
	Actions       map[string]lfsAction `json:"actions,omitempty"`
	Error         *lfsError            `json:"error,omitempty"`
}

type lfsBatchResponse struct {
	Transfer string              `json:"transfer"`
	Objects  []lfsObjectResponse `json:"objects"`
	HashAlgo string              `json:"hash_algo"`
}

// writeLFSError answers in the media type the LFS spec requires for errors. A
// bare http.Error would be reported by the client as an unreadable server
// response rather than as the reason it was refused.
func writeLFSError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", lfsMediaType)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(lfsError{Code: code, Message: fmt.Sprintf(format, args...)})
}

// lfsBatch serves POST {org}/{repo}.git/info/lfs/objects/batch.
func (h *httpHandler) lfsBatch(w http.ResponseWriter, r *http.Request, scope authz.Scope, pp parsedPath) {
	if h.lfs == nil {
		// Explicitly unavailable, not broken: a deployment with no object storage
		// says so, the way the release asset RPCs do.
		writeLFSError(w, http.StatusNotImplemented, "this deployment has no object storage configured, so Git LFS is not available")
		return
	}
	var req lfsBatchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, lfsMaxBatchBody)).Decode(&req); err != nil {
		writeLFSError(w, http.StatusBadRequest, "malformed batch request: %v", err)
		return
	}
	if req.Operation != "upload" && req.Operation != "download" {
		// Only the two operations that move objects. "verify" and the other
		// optional transfer adapters are not implemented, and saying so beats
		// answering as if they were.
		writeLFSError(w, http.StatusUnprocessableEntity, "unsupported LFS operation %q: this server does upload and download", req.Operation)
		return
	}
	if len(req.Transfers) > 0 && !containsString(req.Transfers, "basic") {
		writeLFSError(w, http.StatusUnprocessableEntity, "this server supports only the basic transfer adapter, not %s", strings.Join(req.Transfers, ", "))
		return
	}
	if req.HashAlgo != "" && req.HashAlgo != "sha256" {
		writeLFSError(w, http.StatusUnprocessableEntity, "this server hashes objects with sha256, not %s", req.HashAlgo)
		return
	}

	ctx := r.Context()
	repoID, defaultBranch, err := h.lfs.RepoForLFS(ctx, scope.OrgID, pp.repo)
	if err != nil {
		writeLFSError(w, http.StatusNotFound, "repository %s: not found", pp.repo)
		return
	}

	// The authorization, which is the reason this endpoint lives on the git
	// handler at all. A download is the read git-upload-pack is checked for, so
	// it is checked with no refs, exactly as that is. An upload is a write, and
	// CapFunc speaks refs — so it is checked against the ref the client declared
	// it is pushing, which is what makes the grant, branch-lock, archive and
	// collaborator-role guards all apply to an LFS upload without any of them
	// being taught about LFS.
	ref := ""
	if req.Operation == "upload" {
		ref = "refs/heads/" + defaultBranch
		if req.Ref != nil && req.Ref.Name != "" {
			ref = req.Ref.Name
		}
	}
	if err := h.authorizeLFS(r, scope, pp, ref); err != nil {
		writeLFSError(w, http.StatusForbidden, "%s", err.Error())
		return
	}

	resp := lfsBatchResponse{Transfer: "basic", HashAlgo: "sha256", Objects: make([]lfsObjectResponse, 0, len(req.Objects))}
	for _, o := range req.Objects {
		out := lfsObjectResponse{OID: o.OID, Size: o.Size}
		switch {
		case !lfsOIDRe.MatchString(o.OID):
			out.Error = &lfsError{Code: http.StatusUnprocessableEntity, Message: "not a sha256 object id"}
		case req.Operation == "upload" && o.Size > h.lfs.MaxObjectBytes():
			// Refused here, before any href is handed out, so no byte of an
			// oversized object is ever sent let alone stored.
			out.Error = &lfsError{
				Code:    http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("%d bytes exceeds this deployment's %d byte limit for a single LFS object", o.Size, h.lfs.MaxObjectBytes()),
			}
		default:
			size, have, err := h.lfs.LFSObject(ctx, repoID, o.OID)
			if err != nil {
				out.Error = &lfsError{Code: http.StatusInternalServerError, Message: err.Error()}
				break
			}
			switch {
			case req.Operation == "upload" && have:
				// Already here: an object entry with no actions is how the spec
				// says "nothing to do", and the client skips it.
				out.Size = size
				out.Authenticated = true
			case req.Operation == "upload":
				out.Authenticated = true
				out.Actions = map[string]lfsAction{"upload": h.lfsAction(r, pp, o.OID, ref)}
			case !have:
				out.Error = &lfsError{Code: http.StatusNotFound, Message: "this repository does not have that object"}
			default:
				out.Size = size
				out.Authenticated = true
				out.Actions = map[string]lfsAction{"download": h.lfsAction(r, pp, o.OID, "")}
			}
		}
		resp.Objects = append(resp.Objects, out)
	}

	w.Header().Set("Content-Type", lfsMediaType)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// lfsTransfer serves PUT and GET {org}/{repo}.git/info/lfs/objects/{oid}.
func (h *httpHandler) lfsTransfer(w http.ResponseWriter, r *http.Request, scope authz.Scope, pp parsedPath) {
	if h.lfs == nil {
		writeLFSError(w, http.StatusNotImplemented, "this deployment has no object storage configured, so Git LFS is not available")
		return
	}
	oid := strings.TrimPrefix(pp.op, lfsObjectsPath)
	if !lfsOIDRe.MatchString(oid) {
		writeLFSError(w, http.StatusUnprocessableEntity, "%q is not a sha256 object id", oid)
		return
	}
	ctx := r.Context()
	repoID, defaultBranch, err := h.lfs.RepoForLFS(ctx, scope.OrgID, pp.repo)
	if err != nil {
		writeLFSError(w, http.StatusNotFound, "repository %s: not found", pp.repo)
		return
	}

	if r.Method == http.MethodGet {
		if err := h.authorizeLFS(r, scope, pp, ""); err != nil {
			writeLFSError(w, http.StatusForbidden, "%s", err.Error())
			return
		}
		rc, size, err := h.lfs.OpenObject(ctx, scope.OrgID, repoID, oid)
		if err != nil {
			if errors.Is(err, ErrLFSObjectNotFound) {
				writeLFSError(w, http.StatusNotFound, "this repository does not have object %s", oid)
				return
			}
			writeLFSError(w, http.StatusInternalServerError, "%s", err.Error())
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
		w.WriteHeader(http.StatusOK)
		// A copy failure here is a client that went away mid-download; the status
		// line is already sent, so there is nothing to report but the log.
		_, _ = io.Copy(w, rc)
		return
	}

	// An upload. The ref rides in the href this server itself handed out at
	// batch time, so the write is authorized against the same ref the batch call
	// was authorized against. A client that kept only the oid gets the default
	// branch instead. Neither is trusted as a claim about access: the ref is
	// input to CapFunc, which is the thing that decides, and naming a ref you
	// are allowed to write is what pushing is.
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "refs/heads/" + defaultBranch
	}
	if err := h.authorizeLFS(r, scope, pp, ref); err != nil {
		writeLFSError(w, http.StatusForbidden, "%s", err.Error())
		return
	}
	if r.ContentLength > h.lfs.MaxObjectBytes() {
		writeLFSError(w, http.StatusRequestEntityTooLarge,
			"%d bytes exceeds this deployment's %d byte limit for a single LFS object",
			r.ContentLength, h.lfs.MaxObjectBytes())
		return
	}
	if _, err := h.lfs.PutObject(ctx, scope.OrgID, repoID, oid, r.Body, r.ContentLength); err != nil {
		switch {
		case errors.Is(err, ErrLFSObjectTooLarge):
			writeLFSError(w, http.StatusRequestEntityTooLarge, "%s", err.Error())
		case errors.Is(err, ErrLFSOIDMismatch):
			writeLFSError(w, http.StatusUnprocessableEntity, "%s", err.Error())
		default:
			writeLFSError(w, http.StatusInternalServerError, "%s", err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusOK)
}

// authorizeLFS applies the transport's own capability check. An empty ref is a
// read; a ref is a write to that ref.
func (h *httpHandler) authorizeLFS(r *http.Request, scope authz.Scope, pp parsedPath, ref string) error {
	var refs []string
	if ref != "" {
		refs = []string{ref}
	}
	return h.caps(r.Context(), scope, scope.OrgID, pp.repo, refs)
}

// lfsAction builds the href and headers a client uses for one object.
func (h *httpHandler) lfsAction(r *http.Request, pp parsedPath, oid, ref string) lfsAction {
	href := fmt.Sprintf("%s://%s/%s/%s.git/%s%s", lfsScheme(r), r.Host, pp.orgRef, pp.repo, lfsObjectsPath, oid)
	if ref != "" {
		href += "?ref=" + ref
	}
	a := lfsAction{Href: href}
	// The client's own Authorization header is handed back for it to replay.
	// Without it git-lfs goes to the credential helper for the transfer, which
	// on a non-interactive runner has nothing to offer and the transfer fails
	// with a 401 that looks like a server fault. It travels only back to the
	// client that just sent it, on the same connection, and grants nothing it
	// did not already hold — but it is the reason this endpoint must not be
	// served in plaintext outside the cluster, which is what the TLS port is
	// for.
	if auth := r.Header.Get("Authorization"); auth != "" {
		a.Header = map[string]string{"Authorization": auth}
	}
	return a
}

// lfsScheme reports the scheme a client must use to reach this server. The href
// has to be absolute — git-lfs refuses a relative one — so it cannot be left to
// the client to work out, and a request arriving over plaintext inside the
// cluster must not be told to come back over TLS or the transfer would dial a
// port that may not be published.
func lfsScheme(r *http.Request) string {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
