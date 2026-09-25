package gitops_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
)

// lfsFixture is the whole LFS surface under test: a git data root with a bare
// repository per organization, the object store behind it, and the git
// smart-HTTP transport serving both git and LFS — the same handler, the same
// AuthFunc, the same CapFunc. That sharing is the point: LFS must not be a
// second door into a repository, so these tests drive it through the door the
// git transport uses and nothing else.
type lfsFixture struct {
	root  string
	store *gitops.LFSStore
	blobs *blobstore.Client
	pool  *pgxpool.Pool
	srv   *httptest.Server

	// orgs maps a Basic-auth username to the organization that credential acts
	// in. The username decides the organization and the path never does, which
	// is how "another organization's credential" is expressed here: user "b"
	// lands in organization B however org A's path is spelled in the URL.
	orgs map[string]uuid.UUID

	mu sync.Mutex
	// capCalls records every CapFunc invocation the transport made, so a test
	// can assert that an LFS upload was authorized as a write against a real
	// ref rather than waved through.
	capCalls []capCall
	// deny, when set, is the error CapFunc returns — a stand-in for any of the
	// real guards (grant, archive, branch lock, collaborator role) refusing.
	deny error
}

type capCall struct {
	repo string
	refs []string
}

func newLFSFixture(t *testing.T, maxObjectBytes int64) *lfsFixture {
	t.Helper()
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	url := gitopsDBURL(t)
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform schema: %v", err)
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// A bucket per run: LFS objects share a deployment's object storage with
	// release assets and CI artifacts, so a test that reached across prefixes
	// could take another suite's objects with it.
	blobs, err := blobstore.New(ctx, blobstore.Options{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		Bucket:    "novaforge-test-lfs-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("connect object storage: %v", err)
	}

	f := &lfsFixture{
		root:  t.TempDir(),
		store: gitops.NewLFSStore(pool, blobs, maxObjectBytes),
		blobs: blobs,
		pool:  pool,
		orgs:  map[string]uuid.UUID{},
	}
	auth := func(ctx context.Context, user, pass, orgRef string) (authz.Scope, error) {
		org, ok := f.orgs[user]
		if !ok {
			return authz.Scope{}, errors.New("no such credential")
		}
		return authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.capCalls = append(f.capCalls, capCall{repo: repo, refs: refs})
		return f.deny
	}
	f.srv = httptest.NewServer(gitops.NewHTTPHandlerWithLFS(f.root, auth, caps, f.store))
	t.Cleanup(f.srv.Close)
	return f
}

// newRepo registers a credential, its organization and a bare repository, both
// on disk and in the repositories table — an LFS object is recorded against a
// repository id, so the row has to exist.
func (f *lfsFixture) newRepo(t *testing.T, user, name string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	orgID, ok := f.orgs[user]
	if !ok {
		orgID = uuid.New()
		f.orgs[user] = orgID
	}
	repoID := uuid.New()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO gitplatform.repositories (id, org_id, name) VALUES ($1, $2, $3)`,
		repoID, orgID, name); err != nil {
		t.Fatalf("insert repository: %v", err)
	}
	if _, err := gitops.Init(f.root, orgID, name); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return orgID, repoID
}

func (f *lfsFixture) remote(user string, orgID uuid.UUID, repo string) string {
	return fmt.Sprintf("http://%s:x@%s/%s/%s.git", user, f.srv.Listener.Addr(), orgID, repo)
}

func (f *lfsFixture) setDeny(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deny = err
}

func (f *lfsFixture) calls() []capCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capCall(nil), f.capCalls...)
}

// TestLFSRoundTrip is the criterion: an unmodified git lfs client pushes a large
// file and a fresh clone gets its bytes back exactly, with the Git repository
// holding the pointer and the payload living in object storage.
//
// It drives the real client rather than speaking the batch protocol itself,
// because every part of this that is easy to get wrong is a disagreement with
// what the client actually sends: the endpoint it derives from the remote URL,
// the credential it reuses, the absolute href it insists on.
func TestLFSRoundTrip(t *testing.T) {
	if out, err := exec.Command("git", "lfs", "version").CombinedOutput(); err != nil {
		// Never a silent pass: without the client this proves nothing, and the
		// skip has to say so rather than reading as a green round trip.
		t.Skipf("git lfs is not installed, so the LFS round trip is NOT proven here: %v — %s", err, out)
	}
	f := newLFSFixture(t, 64<<20)
	orgID, repoID := f.newRepo(t, "dev", "assets")
	remote := f.remote("dev", orgID, "assets")

	// A HOME of its own: the workstation's global git config may carry a
	// credential helper or an lfs configuration, and this test must prove what
	// the server does, not what the developer's machine is set up to do.
	env := []string{
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}

	work := filepath.Join(t.TempDir(), "work")
	runCmdEnv(t, "", env, "git", "clone", remote, work)
	// --local, never global: installing the filters system-wide would change the
	// machine running the tests.
	runCmdEnv(t, work, env, "git", "lfs", "install", "--local")
	runCmdEnv(t, work, env, "git", "lfs", "track", "*.bin")

	payload := make([]byte, 5<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("random payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "big.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	runCmdEnv(t, work, env, "git", "add", ".gitattributes", "big.bin")
	runCmdEnv(t, work, env, "git", "-c", "user.email=a@b.c", "-c", "user.name=T", "commit", "-m", "a large binary")
	runCmdEnv(t, work, env, "git", "push", "origin", "HEAD:refs/heads/main")

	// The Git repository holds the pointer, not the payload.
	bare := filepath.Join(f.root, orgID.String(), "assets.git")
	committed := runGit(t, bare, "cat-file", "-p", "main:big.bin")
	if !strings.HasPrefix(committed, "version https://git-lfs.github.com/spec/v1") {
		t.Fatalf("the committed blob is not an LFS pointer, so the payload went into git:\n%.200s", committed)
	}
	if len(committed) > 1024 {
		t.Fatalf("the committed blob is %d bytes; a pointer is a few hundred", len(committed))
	}
	// And the whole bare repository is nowhere near the payload's size, which is
	// the check a pointer-shaped blob alone would not give: 5 MiB of random data
	// does not compress, so a repository holding it could not be this small.
	if size := dirSize(t, bare); size > 1<<20 {
		t.Fatalf("the bare repository is %d bytes with a 5 MiB LFS file pushed: the payload is in the git directory", size)
	}

	// The payload is in object storage, recorded against the repository.
	sum := sha256.Sum256(payload)
	oid := hex.EncodeToString(sum[:])
	size, ok, err := f.store.LFSObject(context.Background(), repoID, oid)
	if err != nil {
		t.Fatalf("LFSObject: %v", err)
	}
	if !ok {
		t.Fatalf("the pushed object %s was not recorded for repository %s", oid, repoID)
	}
	if size != int64(len(payload)) {
		t.Fatalf("the object is recorded as %d bytes, want %d", size, len(payload))
	}

	// A fresh clone gets the bytes back. The filters are installed after the
	// clone because they are not installed globally here, so the checkout leaves
	// pointers and `lfs pull` is what fetches through the server.
	fresh := filepath.Join(t.TempDir(), "fresh")
	runCmdEnv(t, "", env, "git", "clone", remote, fresh)
	runCmdEnv(t, fresh, env, "git", "lfs", "install", "--local")
	runCmdEnv(t, fresh, env, "git", "lfs", "pull")
	got, err := os.ReadFile(filepath.Join(fresh, "big.bin"))
	if err != nil {
		t.Fatalf("read the cloned file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the cloned file is %d bytes and does not match the %d bytes pushed", len(got), len(payload))
	}
}

// TestLFSQuotaAndOwnership pins the two ways an LFS store becomes a hole: an
// object reachable with another organization's credential, and an upload nobody
// bounded. Both are checked over HTTP rather than against the store directly,
// because the transport is where the credential and the limit are applied.
func TestLFSQuotaAndOwnership(t *testing.T) {
	const maxBytes = 1 << 20
	f := newLFSFixture(t, maxBytes)
	orgA, repoA := f.newRepo(t, "a", "alpha")
	orgB, _ := f.newRepo(t, "b", "beta")
	ctx := context.Background()

	payload := []byte(strings.Repeat("novaforge-lfs-payload", 64))
	sum := sha256.Sum256(payload)
	oid := hex.EncodeToString(sum[:])

	t.Run("an upload declaring more than the limit is refused before any bytes are stored", func(t *testing.T) {
		big := bytes.Repeat([]byte("x"), 32)
		bigSum := sha256.Sum256(big)
		bigOID := hex.EncodeToString(bigSum[:])
		resp := f.batch(t, "a", orgA, "alpha", "upload", "refs/heads/main", batchObject{OID: bigOID, Size: maxBytes + 1})
		if len(resp.Objects) != 1 {
			t.Fatalf("batch returned %d objects, want 1", len(resp.Objects))
		}
		o := resp.Objects[0]
		if o.Error == nil {
			t.Fatalf("an upload of %d bytes was accepted with a %d byte limit: actions %+v", maxBytes+1, maxBytes, o.Actions)
		}
		if o.Actions != nil {
			t.Fatalf("the oversized object was refused and handed an upload action anyway: %+v", o.Actions)
		}
		_, ok, err := f.store.LFSObject(ctx, repoA, bigOID)
		if err != nil {
			t.Fatalf("LFSObject: %v", err)
		}
		if ok {
			t.Fatal("the refused object was recorded anyway")
		}
	})

	t.Run("an object transfer larger than the limit is refused at the transport", func(t *testing.T) {
		body := bytes.Repeat([]byte("y"), maxBytes+1)
		sum := sha256.Sum256(body)
		overOID := hex.EncodeToString(sum[:])
		code, _ := f.transfer(t, http.MethodPut, "a", orgA, "alpha", overOID, body)
		if code != http.StatusRequestEntityTooLarge {
			t.Fatalf("a %d byte upload against a %d byte limit answered %d, want 413", len(body), maxBytes, code)
		}
		_, ok, err := f.store.LFSObject(ctx, repoA, overOID)
		if err != nil {
			t.Fatalf("LFSObject: %v", err)
		}
		if ok {
			t.Fatal("the oversized transfer was recorded anyway")
		}
	})

	t.Run("a body that does not hash to the oid is refused", func(t *testing.T) {
		code, _ := f.transfer(t, http.MethodPut, "a", orgA, "alpha", oid, []byte("not the payload"))
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("a body that does not hash to the oid answered %d, want 422", code)
		}
		_, ok, err := f.store.LFSObject(ctx, repoA, oid)
		if err != nil {
			t.Fatalf("LFSObject: %v", err)
		}
		if ok {
			t.Fatal("an object whose bytes did not match its oid was recorded")
		}
	})

	// Now a real upload, through the batch endpoint and the href it hands back.
	resp := f.batch(t, "a", orgA, "alpha", "upload", "refs/heads/main", batchObject{OID: oid, Size: int64(len(payload))})
	if len(resp.Objects) != 1 || resp.Objects[0].Error != nil {
		t.Fatalf("batch refused a legitimate upload: %+v", resp.Objects)
	}
	action, ok := resp.Objects[0].Actions["upload"]
	if !ok {
		t.Fatalf("batch returned no upload action: %+v", resp.Objects[0].Actions)
	}
	if !strings.HasPrefix(action.Href, "http://") {
		t.Fatalf("the upload href %q is not absolute; git lfs requires an absolute href", action.Href)
	}
	if code, body := f.put(t, action, payload); code != http.StatusOK {
		t.Fatalf("PUT to the upload href answered %d: %s", code, body)
	}

	t.Run("the upload was authorized as a write against the ref the client declared", func(t *testing.T) {
		var seen bool
		for _, c := range f.calls() {
			if c.repo == "alpha" && len(c.refs) == 1 && c.refs[0] == "refs/heads/main" {
				seen = true
			}
		}
		if !seen {
			t.Fatalf("no capability check carried the declared ref, so an LFS upload is not held to write access: %+v", f.calls())
		}
	})

	t.Run("the owner downloads the bytes", func(t *testing.T) {
		code, got := f.transfer(t, http.MethodGet, "a", orgA, "alpha", oid, nil)
		if code != http.StatusOK {
			t.Fatalf("the owner's download answered %d: %s", code, got)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("the download returned %d bytes, want %d", len(got), len(payload))
		}
	})

	t.Run("another organization's credential reaches nothing", func(t *testing.T) {
		// Organization B's credential, naming organization A's path. The scope
		// comes from the credential, so the path buys nothing.
		code, got := f.transfer(t, http.MethodGet, "b", orgA, "alpha", oid, nil)
		if code == http.StatusOK {
			t.Fatalf("organization B downloaded organization A's object: %d bytes", len(got))
		}
		if bytes.Contains(got, payload[:32]) {
			t.Fatalf("the refusal body carries the payload: %s", got)
		}
		// And through the batch endpoint, against B's own repository: the object
		// is simply not there, which is what another organization's object must
		// look like.
		resp := f.batch(t, "b", orgB, "beta", "download", "", batchObject{OID: oid, Size: int64(len(payload))})
		if len(resp.Objects) != 1 {
			t.Fatalf("batch returned %d objects, want 1", len(resp.Objects))
		}
		if resp.Objects[0].Error == nil {
			t.Fatalf("organization B's batch found organization A's object: %+v", resp.Objects[0].Actions)
		}
	})

	t.Run("a capability refusal stops LFS as it stops a push", func(t *testing.T) {
		f.setDeny(errors.New("this repository is archived"))
		defer f.setDeny(nil)
		code, body := f.transfer(t, http.MethodGet, "a", orgA, "alpha", oid, nil)
		if code != http.StatusForbidden {
			t.Fatalf("a denied download answered %d, want 403: %s", code, body)
		}
		if !strings.Contains(string(body), "archived") {
			t.Fatalf("the refusal does not carry the reason: %s", body)
		}
	})
}

// --- the batch protocol, spoken by hand ---

type batchObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type batchAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

type batchResponseObject struct {
	OID     string                 `json:"oid"`
	Size    int64                  `json:"size"`
	Actions map[string]batchAction `json:"actions"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type batchResponse struct {
	Transfer string                `json:"transfer"`
	Objects  []batchResponseObject `json:"objects"`
}

func (f *lfsFixture) batch(t *testing.T, user string, orgID uuid.UUID, repo, op, ref string, objects ...batchObject) batchResponse {
	t.Helper()
	body := map[string]any{
		"operation": op,
		"transfers": []string{"basic"},
		"objects":   objects,
		"hash_algo": "sha256",
	}
	if ref != "" {
		body["ref"] = map[string]string{"name": ref}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("http://%s/%s/%s.git/info/lfs/objects/batch", f.srv.Listener.Addr(), orgID, repo)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, "x")
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	req.Header.Set("Accept", "application/vnd.git-lfs+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("batch request: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch answered %d: %s", resp.StatusCode, out)
	}
	var parsed batchResponse
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse batch response %q: %v", out, err)
	}
	return parsed
}

// transfer addresses the object endpoint directly, the way a client that kept a
// href would.
func (f *lfsFixture) transfer(t *testing.T, method, user string, orgID uuid.UUID, repo, oid string, body []byte) (int, []byte) {
	t.Helper()
	url := fmt.Sprintf("http://%s/%s/%s.git/info/lfs/objects/%s", f.srv.Listener.Addr(), orgID, repo, oid)
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, "x")
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// put follows an upload action exactly as a client would, including the headers
// the batch response told it to send.
func (f *lfsFixture) put(t *testing.T, action batchAction, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, action.Href, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range action.Header {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", action.Href, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return total
}
