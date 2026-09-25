package gitops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors a caller has to be able to tell apart. A missing tag is the caller's
// mistake and is fixable by pushing the tag; a missing release or asset is an
// absence, and is also what another organization's release looks like from here.
var (
	// ErrNoSuchTag means the repository has no such tag. A release exists to be
	// downloaded from, so it cannot be created on a version that does not exist:
	// the link would point at nothing and nobody would find out until they
	// clicked it.
	ErrNoSuchTag = errors.New("no such tag in this repository")
	// ErrReleaseNotFound is an absent release, including one held by another
	// organization — the two are deliberately indistinguishable.
	ErrReleaseNotFound = errors.New("no such release")
	// ErrAssetNotFound is an absent asset, on the same reasoning.
	ErrAssetNotFound = errors.New("no such release asset")
	// ErrReleaseExists is a second release on one tag.
	ErrReleaseExists = errors.New("this tag already has a release")
)

// Release is a tag published for download, with the files published alongside it.
type Release struct {
	ID        uuid.UUID
	RepoID    uuid.UUID
	Tag       string
	Name      string
	Body      string
	CreatedAt time.Time
	Assets    []Asset
}

// Asset is one downloadable file on a release. BlobKey is the object's key in the
// deployment's bucket and is carried so deletion has something exact to delete.
type Asset struct {
	ID          uuid.UUID
	ReleaseID   uuid.UUID
	Name        string
	Size        int64
	ContentType string
	BlobKey     string
}

// BlobStore is the slice of blobstore.Client that releases need.
//
// It is an interface so this package does not depend on the S3 client, and so a
// deployment without object storage configured can hold a nil one and refuse
// asset operations with a clear reason instead of failing to start. It is not
// here to be doubled in tests: the tests use real MinIO, because the property
// being proven — that deleting a release deletes its objects — is a property of
// the object store and an in-process double would agree with anything.
type BlobStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// ReleaseStore holds releases and their assets. It is separate from Server for
// the same reason CollaboratorStore is: the store is usable — and testable —
// without the whole RPC surface, and it needs the git data root as well as the
// database because a release is only valid on a tag the repository actually has.
type ReleaseStore struct {
	pool  *pgxpool.Pool
	root  string
	blobs BlobStore
}

// NewReleaseStore builds the store. blobs may be nil, in which case releases can
// still be created, listed and deleted but assets cannot be uploaded or
// downloaded — a deployment with no object storage says so rather than accepting
// an upload it has nowhere to put.
func NewReleaseStore(pool *pgxpool.Pool, root string, blobs BlobStore) *ReleaseStore {
	return &ReleaseStore{pool: pool, root: root, blobs: blobs}
}

// HasBlobStore reports whether asset upload and download are available in this
// deployment. The API uses it to answer "not available here" rather than an
// internal error, which is a different thing for whoever reads it.
func (s *ReleaseStore) HasBlobStore() bool { return s.blobs != nil }

// assetKey is the object key for one asset.
//
// The organization and the repository are both in the key, ahead of anything a
// caller chooses. Keying on the tag and the file name alone — the obvious
// shape, and the one a download URL suggests — puts every organization's
// "v1.0.0/build.bin" at the same key: one organization's upload would overwrite
// another's release, and a guessed key would download it. Ids are not guessable
// and are scoped, so neither is possible here.
func assetKey(orgID, repoID, releaseID, assetID uuid.UUID) string {
	return fmt.Sprintf("org/%s/repo/%s/release/%s/%s", orgID, repoID, releaseID, assetID)
}

// repoForRelease resolves a repository from its name or its id within orgID.
//
// It duplicates Server.repoByName rather than calling it because the store must
// work without a Server, and because it returns a plain error instead of a gRPC
// status — the RPC layer maps it, and a store used from anywhere else should not
// have to unwrap a status to find out what happened.
func (s *ReleaseStore) repoForRelease(ctx context.Context, orgID uuid.UUID, ref string) (uuid.UUID, string, error) {
	if ref == "" {
		return uuid.Nil, "", errors.New("repo is required")
	}
	column, value := "name", any(ref)
	if id, err := uuid.Parse(ref); err == nil {
		column, value = "id", any(id)
	}
	var id uuid.UUID
	var name string
	// The organization comes from the caller's scope, never from the request, so
	// naming another organization's repository by its id finds nothing.
	err := s.pool.QueryRow(ctx,
		`SELECT id, name FROM gitplatform.repositories WHERE org_id = $1 AND `+column+` = $2`,
		orgID, value).Scan(&id, &name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", fmt.Errorf("repository %q: %w", ref, ErrReleaseNotFound)
		}
		return uuid.Nil, "", fmt.Errorf("lookup repository: %w", err)
	}
	return id, name, nil
}

// CreateRelease publishes tag as a release. The tag has to exist: Git owns refs,
// so this asks the repository rather than a table.
func (s *ReleaseStore) CreateRelease(ctx context.Context, orgID uuid.UUID, repoRef, tag, name, body string) (Release, error) {
	if strings.TrimSpace(tag) == "" {
		return Release{}, errors.New("tag is required")
	}
	repoID, repoName, err := s.repoForRelease(ctx, orgID, repoRef)
	if err != nil {
		return Release{}, err
	}
	if err := s.requireTag(orgID, repoName, tag); err != nil {
		return Release{}, err
	}

	rel := Release{ID: uuid.New(), RepoID: repoID, Tag: tag, Name: name, Body: body, Assets: []Asset{}}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO gitplatform.releases (id, org_id, repo_id, tag, name, body)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		rel.ID, orgID, repoID, tag, name, body).Scan(&rel.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Release{}, fmt.Errorf("%s: %w", tag, ErrReleaseExists)
		}
		return Release{}, fmt.Errorf("create release: %w", err)
	}
	return rel, nil
}

// requireTag refuses unless the repository has the tag. The ref is addressed in
// full — refs/tags/<tag> — so a branch of the same name cannot stand in for it,
// and so a caller cannot pass something like "HEAD" or "main~2" and have a
// release created on a moving target.
func (s *ReleaseStore) requireTag(orgID uuid.UUID, repoName, tag string) error {
	path, err := resolvePath(s.root, orgID, repoName)
	if err != nil {
		return err
	}
	if _, err := run(path, "rev-parse", "--verify", "refs/tags/"+tag); err != nil {
		return fmt.Errorf("%s: %w", tag, ErrNoSuchTag)
	}
	return nil
}

// ListReleases returns a repository's releases, newest first, each with its
// assets.
func (s *ReleaseStore) ListReleases(ctx context.Context, orgID uuid.UUID, repoRef string) ([]Release, error) {
	repoID, _, err := s.repoForRelease(ctx, orgID, repoRef)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, repo_id, tag, name, body, created_at
		FROM gitplatform.releases
		WHERE org_id = $1 AND repo_id = $2
		ORDER BY created_at DESC`, orgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close()
	out := []Release{}
	byID := map[uuid.UUID]int{}
	for rows.Next() {
		var r Release
		if err := rows.Scan(&r.ID, &r.RepoID, &r.Tag, &r.Name, &r.Body, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		r.Assets = []Asset{}
		byID[r.ID] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	ids := make([]uuid.UUID, 0, len(out))
	for _, r := range out {
		ids = append(ids, r.ID)
	}
	// One query for every release's assets rather than one per release: a
	// repository with a long release history is the normal case and the per-row
	// version made the listing cost grow with it.
	assetRows, err := s.pool.Query(ctx, `
		SELECT id, release_id, name, size, content_type, blob_key
		FROM gitplatform.release_assets
		WHERE release_id = ANY($1)
		ORDER BY name`, ids)
	if err != nil {
		return nil, fmt.Errorf("list release assets: %w", err)
	}
	defer assetRows.Close()
	for assetRows.Next() {
		var a Asset
		if err := assetRows.Scan(&a.ID, &a.ReleaseID, &a.Name, &a.Size, &a.ContentType, &a.BlobKey); err != nil {
			return nil, fmt.Errorf("scan release asset: %w", err)
		}
		if i, ok := byID[a.ReleaseID]; ok {
			out[i].Assets = append(out[i].Assets, a)
		}
	}
	return out, assetRows.Err()
}

// GetRelease returns one release by tag, with its assets.
func (s *ReleaseStore) GetRelease(ctx context.Context, orgID uuid.UUID, repoRef, tag string) (Release, error) {
	repoID, _, err := s.repoForRelease(ctx, orgID, repoRef)
	if err != nil {
		return Release{}, err
	}
	var rel Release
	err = s.pool.QueryRow(ctx, `
		SELECT id, repo_id, tag, name, body, created_at
		FROM gitplatform.releases
		WHERE org_id = $1 AND repo_id = $2 AND tag = $3`, orgID, repoID, tag).
		Scan(&rel.ID, &rel.RepoID, &rel.Tag, &rel.Name, &rel.Body, &rel.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Release{}, fmt.Errorf("%s: %w", tag, ErrReleaseNotFound)
		}
		return Release{}, fmt.Errorf("get release: %w", err)
	}
	rel.Assets, err = s.assetsOf(ctx, rel.ID)
	if err != nil {
		return Release{}, err
	}
	return rel, nil
}

func (s *ReleaseStore) assetsOf(ctx context.Context, releaseID uuid.UUID) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, release_id, name, size, content_type, blob_key
		FROM gitplatform.release_assets WHERE release_id = $1 ORDER BY name`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("list release assets: %w", err)
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.ReleaseID, &a.Name, &a.Size, &a.ContentType, &a.BlobKey); err != nil {
			return nil, fmt.Errorf("scan release asset: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddAsset uploads one file onto a release and records it.
//
// r is streamed to object storage, never read into memory: a release asset is a
// build output and is routinely hundreds of megabytes. A negative size streams
// with an unknown length, which the multipart uploader handles, and the recorded
// size is then whatever arrived.
func (s *ReleaseStore) AddAsset(ctx context.Context, orgID, releaseID uuid.UUID, name, contentType string, r io.Reader, size int64) (Asset, error) {
	if s.blobs == nil {
		return Asset{}, errors.New("this deployment has no object storage configured, so release assets cannot be stored")
	}
	if strings.TrimSpace(name) == "" {
		return Asset{}, errors.New("an asset needs a name")
	}
	// A name is part of a download URL and of nothing else, so it must not be
	// able to address anything: a path separator or a traversal segment in it
	// would make the asset addressable as another release's.
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return Asset{}, fmt.Errorf("asset name %q may not contain a path", name)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// The release is confirmed inside the caller's organization before anything
	// is written, so an asset cannot be attached to another organization's
	// release by naming its id.
	var repoID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT repo_id FROM gitplatform.releases WHERE id = $1 AND org_id = $2`,
		releaseID, orgID).Scan(&repoID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Asset{}, fmt.Errorf("%s: %w", releaseID, ErrReleaseNotFound)
		}
		return Asset{}, fmt.Errorf("lookup release: %w", err)
	}

	assetID := uuid.New()
	a := Asset{
		ID: assetID, ReleaseID: releaseID, Name: name,
		Size: size, ContentType: contentType,
		BlobKey: assetKey(orgID, repoID, releaseID, assetID),
	}

	// Counting on the way past means the recorded size is what actually arrived,
	// not what the uploader claimed. A declared Content-Length that did not match
	// the body would otherwise be published as the download's size and every
	// client would report a truncated file.
	counter := &countingReader{r: r}
	if err := s.blobs.Put(ctx, a.BlobKey, counter, size, contentType); err != nil {
		return Asset{}, fmt.Errorf("store release asset: %w", err)
	}
	a.Size = counter.n

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO gitplatform.release_assets (id, release_id, name, size, content_type, blob_key)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		a.ID, a.ReleaseID, a.Name, a.Size, a.ContentType, a.BlobKey); err != nil {
		// The row is what makes the object reachable, so an object with no row is
		// unreachable and would leak. Remove it before reporting the failure.
		if delErr := s.blobs.Delete(ctx, a.BlobKey); delErr != nil {
			return Asset{}, fmt.Errorf("record release asset: %w (and its object %s could not be removed: %v)", err, a.BlobKey, delErr)
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Asset{}, fmt.Errorf("this release already has an asset named %q", name)
		}
		return Asset{}, fmt.Errorf("record release asset: %w", err)
	}
	return a, nil
}

// countingReader records how many bytes were actually read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// OpenAsset opens one asset for reading, addressed the way a download URL does:
// by repository, tag and file name.
//
// It returns a reader, not bytes. The asset is a build output and buffering it
// would put its whole size in the process's memory once per concurrent download,
// which is how a service gets killed by serving a popular release.
func (s *ReleaseStore) OpenAsset(ctx context.Context, orgID uuid.UUID, repoRef, tag, name string) (Asset, io.ReadCloser, error) {
	if s.blobs == nil {
		return Asset{}, nil, errors.New("this deployment has no object storage configured, so release assets cannot be served")
	}
	repoID, _, err := s.repoForRelease(ctx, orgID, repoRef)
	if err != nil {
		return Asset{}, nil, err
	}
	var a Asset
	// The join carries the organization predicate, so an asset of another
	// organization reads as absent rather than as forbidden.
	err = s.pool.QueryRow(ctx, `
		SELECT a.id, a.release_id, a.name, a.size, a.content_type, a.blob_key
		FROM gitplatform.release_assets a
		JOIN gitplatform.releases r ON r.id = a.release_id
		WHERE r.org_id = $1 AND r.repo_id = $2 AND r.tag = $3 AND a.name = $4`,
		orgID, repoID, tag, name).
		Scan(&a.ID, &a.ReleaseID, &a.Name, &a.Size, &a.ContentType, &a.BlobKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Asset{}, nil, fmt.Errorf("%s: %w", name, ErrAssetNotFound)
		}
		return Asset{}, nil, fmt.Errorf("lookup release asset: %w", err)
	}
	rc, err := s.blobs.Get(ctx, a.BlobKey)
	if err != nil {
		return Asset{}, nil, fmt.Errorf("open release asset: %w", err)
	}
	return a, rc, nil
}

// DeleteRelease removes a release, its assets' rows and its assets' objects.
//
// The objects go first. An object deleted with its row still present is found
// again by the next delete and retried; a row deleted with its object still
// present leaves an object nothing references, which nothing will ever find and
// which grows the bucket forever. So a failure to delete an object aborts the
// whole operation with the rows intact.
func (s *ReleaseStore) DeleteRelease(ctx context.Context, orgID uuid.UUID, repoRef, tag string) error {
	rel, err := s.GetRelease(ctx, orgID, repoRef, tag)
	if err != nil {
		return err
	}
	if len(rel.Assets) > 0 && s.blobs == nil {
		return errors.New("this release has assets and this deployment has no object storage configured, so it cannot be deleted without leaving them behind")
	}
	for _, a := range rel.Assets {
		if err := s.blobs.Delete(ctx, a.BlobKey); err != nil {
			return fmt.Errorf("delete release asset object %s: %w", a.BlobKey, err)
		}
	}
	// release_assets is removed by the ON DELETE CASCADE on release_id.
	tag2, err := s.pool.Exec(ctx,
		`DELETE FROM gitplatform.releases WHERE id = $1 AND org_id = $2`, rel.ID, orgID)
	if err != nil {
		return fmt.Errorf("delete release: %w", err)
	}
	if tag2.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", tag, ErrReleaseNotFound)
	}
	return nil
}
