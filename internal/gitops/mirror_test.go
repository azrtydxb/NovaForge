package gitops_test

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/tools/go/packages"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
)

// mirrorKEK is the key-encryption key these tests store a remote credential
// under. It is a test value, not a default: a store built with an empty KEK
// refuses to hold a credential rather than storing one in the clear.
var mirrorKEK = []byte("mirror-tests-kek")

// mirrorSuite is a throwaway PostgreSQL database of this package's own, created
// by TestMain and dropped after.
//
// It is deliberately not the cluster's dev database, which the rest of this
// package's tests migrate directly. That one is shared with every other lane
// building against this cluster, and its gitplatform_git tracking table can
// already record a migration version this worktree does not contain —
// golang-migrate then refuses the whole set ("no migration found for version
// N"), which has nothing to do with the code under test. A database of our own
// also means these tests cannot see, or be seen by, another lane's rows.
var mirrorSuite struct {
	url  string
	pool *pgxpool.Pool
}

func TestMain(m *testing.M) {
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		// Without a datastore the database-backed tests skip. That is
		// indistinguishable from a pass in the output, which is why hack/env.sh
		// says so out loud when it cannot discover one.
		os.Exit(m.Run())
	}
	code, err := withMirrorDatabase(admin, m)
	if err != nil {
		panic(err)
	}
	os.Exit(code)
}

func withMirrorDatabase(admin string, m *testing.M) (int, error) {
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, admin)
	if err != nil {
		return 0, fmt.Errorf("connect to create a test database: %w", err)
	}
	defer owner.Close(ctx)

	name := "mirrors_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := owner.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return 0, fmt.Errorf("create test database: %w", err)
	}
	defer func() {
		_, _ = owner.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	}()

	u, err := url.Parse(admin)
	if err != nil {
		return 0, fmt.Errorf("parse TEST_DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	mirrorSuite.url = u.String()

	if err := database.Migrate(mirrorSuite.url, "gitplatform", capability.MigrationsFS); err != nil {
		return 0, err
	}
	if err := database.MigrateAs(mirrorSuite.url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		return 0, fmt.Errorf("migrate gitplatform (repositories, mirrors): %w", err)
	}
	pool, err := database.Connect(ctx, mirrorSuite.url)
	if err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	mirrorSuite.pool = pool
	defer pool.Close()
	return m.Run(), nil
}

func mirrorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if mirrorSuite.pool == nil {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return mirrorSuite.pool
}

// upstreamToken is the credential the source repository's transport demands. It
// is a long, distinctive string so a leak of it into a row or a log line is
// unambiguous when a test greps for it.
const upstreamToken = "upstream-credential-9d3f1c7a-never-log-me"

// upstream is a source repository served over the platform's own smart-HTTP
// transport behind a credential. URL carries no credential: supplying it out of
// band is the whole point of the feature.
type upstream struct {
	URL      string
	Worktree string
	authed   string
}

func serveUpstream(t *testing.T, repoName string) upstream {
	t.Helper()
	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, repoName); err != nil {
		t.Fatalf("init upstream: %v", err)
	}

	auth := func(_ context.Context, _, pass, _ string) (authz.Scope, error) {
		if pass != upstreamToken {
			return authz.Scope{}, fmt.Errorf("invalid credentials")
		}
		return authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"}, nil
	}
	caps := func(context.Context, authz.Scope, uuid.UUID, string, []string) error { return nil }
	server := httptest.NewServer(gitops.NewHTTPHandler(root, auth, caps))
	t.Cleanup(server.Close)

	up := upstream{
		URL: fmt.Sprintf("http://%s/%s/%s.git", server.Listener.Addr().String(), orgID, repoName),
		authed: fmt.Sprintf("http://git:%s@%s/%s/%s.git",
			upstreamToken, server.Listener.Addr().String(), orgID, repoName),
		Worktree: t.TempDir(),
	}

	work := up.Worktree
	runCmd(t, "", "git", "clone", up.authed, work)
	writeFileHTTP(t, filepath.Join(work, "README.md"), "upstream\n")
	runCmd(t, work, "git", "add", "README.md")
	runCmd(t, work, "git", "-c", "user.email=a@b.c", "-c", "user.name=Up", "commit", "-m", "upstream first")
	runCmd(t, work, "git", "tag", "v1.0.0")
	runCmd(t, work, "git", "checkout", "-b", "release")
	writeFileHTTP(t, filepath.Join(work, "ship.txt"), "ship\n")
	runCmd(t, work, "git", "add", "ship.txt")
	runCmd(t, work, "git", "-c", "user.email=a@b.c", "-c", "user.name=Up", "commit", "-m", "upstream release")
	runCmd(t, work, "git", "push", up.authed, "release:refs/heads/release", "main:refs/heads/main", "v1.0.0")
	return up
}

func personIn(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID: orgID, ActorID: uuid.New(), ActorKind: "user", Role: "admin",
	})
}

// TestImportFromRemote is the migration path onto the platform: an existing
// repository elsewhere arrives with its whole history, and a failed import
// leaves nothing at all behind.
func TestImportFromRemote(t *testing.T) {
	pool := mirrorPool(t)
	root := t.TempDir()
	orgID := uuid.New()
	ctx := personIn(orgID)
	store := gitops.NewMirrorStore(pool, root, mirrorKEK)

	up := serveUpstream(t, "source")
	imported, err := store.ImportRepo(ctx, gitops.ImportRequest{
		Name: "imported", Remote: up.URL, Credential: upstreamToken,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.Name != "imported" || imported.OrgID != orgID {
		t.Fatalf("want imported/%s, got %+v", orgID, imported)
	}

	repo, err := gitops.Open(root, orgID, "imported")
	if err != nil {
		t.Fatalf("open imported: %v", err)
	}
	branches, err := repo.Branches()
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if !hasRef(branches, "main") || !hasRef(branches, "release") {
		t.Fatalf("want main and release, got %+v", branches)
	}
	tags, err := repo.Tags()
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if !hasRef(tags, "v1.0.0") {
		t.Fatalf("want tag v1.0.0, got %+v", tags)
	}
	commits, err := repo.Log("release", 10)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(commits) != 2 || commits[0].Message != "upstream release" {
		t.Fatalf("want the upstream history on release, got %+v", commits)
	}

	// A failed import leaves no row and no directory. Port 1 on the loopback is
	// not listening, so the clone fails at connect.
	if _, err := store.ImportRepo(ctx, gitops.ImportRequest{
		Name: "doomed", Remote: "http://127.0.0.1:1/nope.git", Credential: upstreamToken,
	}); err == nil {
		t.Fatal("want an import from an unreachable remote to fail")
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM gitplatform.repositories WHERE org_id = $1 AND name = 'doomed'`,
		orgID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("want no repository row after a failed import, got %d", n)
	}
	assertOnlyRepos(t, root, orgID, "imported.git")

	// An import that fails *after* the clone succeeded is the case that actually
	// tests the throwaway directory. A clone git itself refuses does not: git
	// removes its own target directory when it gives up, so a version of this
	// code with no cleanup at all still leaves nothing behind. This one seeds a
	// repositories row whose directory is absent, so the clone runs to completion
	// and the insert is then refused as a duplicate name.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO gitplatform.repositories (id, org_id, name, default_branch)
		 VALUES ($1, $2, 'taken', 'main')`, uuid.New(), orgID); err != nil {
		t.Fatalf("seed a conflicting repository row: %v", err)
	}
	if _, err := store.ImportRepo(ctx, gitops.ImportRequest{
		Name: "taken", Remote: up.URL, Credential: upstreamToken,
	}); err == nil {
		t.Fatal("want an import onto an existing repository name to fail")
	}
	assertOnlyRepos(t, root, orgID, "imported.git")
}

// assertOnlyRepos fails unless the organization's directory holds exactly want.
// Anything else is a throwaway clone that was not cleaned up, and the next
// import of that name would then fail forever on a directory nothing records.
func assertOnlyRepos(t *testing.T, root string, orgID uuid.UUID, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, orgID.String()))
	if err != nil {
		t.Fatalf("read org directory: %v", err)
	}
	expected := map[string]bool{}
	for _, w := range want {
		expected[w] = true
	}
	for _, e := range entries {
		if !expected[e.Name()] {
			t.Fatalf("a failed import left %s behind", e.Name())
		}
	}
}

// TestMirrorRefresh is the mirror's two halves: upstream's new work arrives when
// the mirrorer runs, and a push here is refused because upstream owns the
// history.
func TestMirrorRefresh(t *testing.T) {
	pool := mirrorPool(t)
	root := t.TempDir()
	orgID := uuid.New()
	ctx := personIn(orgID)
	store := gitops.NewMirrorStore(pool, root, mirrorKEK)

	up := serveUpstream(t, "tracked")
	if _, err := store.ImportRepo(ctx, gitops.ImportRequest{
		Name: "tracked", Remote: up.URL, Credential: upstreamToken,
		Mirror: true, Interval: 0,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	// A new commit upstream, which this side has never seen.
	work := up.Worktree
	runCmd(t, work, "git", "checkout", "main")
	writeFileHTTP(t, filepath.Join(work, "later.txt"), "later\n")
	runCmd(t, work, "git", "add", "later.txt")
	runCmd(t, work, "git", "-c", "user.email=a@b.c", "-c", "user.name=Up", "commit", "-m", "upstream later")
	runCmd(t, work, "git", "push", up.authed, "main:refs/heads/main")

	var logged bytes.Buffer
	mirrorer := &gitops.Mirrorer{
		Store: store,
		Tick:  time.Millisecond,
		Logf:  log.New(&logged, "", 0).Printf,
	}
	refreshed, err := mirrorer.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("want 1 mirror refreshed, got %d (log: %s)", refreshed, logged.String())
	}

	repo, err := gitops.Open(root, orgID, "tracked")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	commits, err := repo.Log("main", 10)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(commits) == 0 || commits[0].Message != "upstream later" {
		t.Fatalf("want the new upstream commit after a refresh, got %+v", commits)
	}

	// A push to a mirror is refused, and the refusal names upstream. This goes
	// through the real transport with the real CapFunc, because a guard tested
	// only as a function is a guard nothing is proven to apply.
	auth := func(context.Context, string, string, string) (authz.Scope, error) {
		return authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"}, nil
	}
	server := httptest.NewServer(gitops.NewHTTPHandler(root, auth, gitops.NewMirrorCapFunc(pool)))
	defer server.Close()
	local := fmt.Sprintf("http://git:x@%s/%s/tracked.git", server.Listener.Addr().String(), orgID)

	clone := t.TempDir()
	runCmd(t, "", "git", "clone", local, clone)
	writeFileHTTP(t, filepath.Join(clone, "mine.txt"), "mine\n")
	runCmd(t, clone, "git", "add", "mine.txt")
	runCmd(t, clone, "git", "-c", "user.email=a@b.c", "-c", "user.name=Me", "commit", "-m", "local work")
	cmd := exec.Command("git", "push", "origin", "HEAD:refs/heads/main")
	cmd.Dir = clone
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("want a push to a mirror to be refused, got success: %s", out)
	}
	if !strings.Contains(string(out), "mirror") || !strings.Contains(string(out), up.URL) {
		t.Fatalf("want the refusal to name upstream %s as the owner of the history, got %s", up.URL, out)
	}
}

// TestMirrorCredentialIsEncryptedAndNeverLogged is the property that makes this
// feature safe to have at all. A mirror credential is a token for somebody
// else's Git host; the two places it classically leaks are the stored remote URL
// (git's own idiom is https://user:token@host) and a log line about a failing
// refresh. Both are checked against the literal value.
func TestMirrorCredentialIsEncryptedAndNeverLogged(t *testing.T) {
	pool := mirrorPool(t)
	root := t.TempDir()
	orgID := uuid.New()
	ctx := personIn(orgID)
	store := gitops.NewMirrorStore(pool, root, mirrorKEK)

	up := serveUpstream(t, "secretive")
	imported, err := store.ImportRepo(ctx, gitops.ImportRequest{
		Name: "secretive", Remote: up.URL, Credential: upstreamToken,
		Mirror: true, Interval: 0,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	var storedRemote string
	var storedCredential []byte
	err = pool.QueryRow(context.Background(),
		`SELECT remote, credential FROM gitplatform.repository_mirrors WHERE repo_id = $1 AND org_id = $2`,
		imported.ID, orgID).Scan(&storedRemote, &storedCredential)
	if err != nil {
		t.Fatalf("read the stored mirror row: %v", err)
	}
	if strings.Contains(storedRemote, upstreamToken) {
		t.Fatalf("the credential is in the stored remote URL in plain text: %s", storedRemote)
	}
	if len(storedCredential) == 0 {
		t.Fatal("want the credential stored as ciphertext, got nothing")
	}
	if bytes.Contains(storedCredential, []byte(upstreamToken)) {
		t.Fatal("the stored credential column contains the credential in plain text")
	}

	// Nothing read back carries it either.
	mirror, err := store.GetMirror(ctx, imported.ID)
	if err != nil {
		t.Fatalf("get mirror: %v", err)
	}
	if !mirror.HasCredential {
		t.Fatal("want has_credential true")
	}
	if strings.Contains(fmt.Sprintf("%+v", mirror), upstreamToken) {
		t.Fatalf("a read of the mirror returned the credential: %+v", mirror)
	}

	// Now make the refresh fail, which is when something gets logged, and check
	// the captured log. A remote nothing is listening on produces git's own error
	// message, which is what a naive implementation would print verbatim after
	// having put the credential into the URL.
	if err := store.SetMirror(ctx, imported.ID, "http://127.0.0.1:1/gone.git", upstreamToken, time.Second); err != nil {
		t.Fatalf("set mirror: %v", err)
	}
	var logged bytes.Buffer
	mirrorer := &gitops.Mirrorer{Store: store, Tick: time.Millisecond, Logf: log.New(&logged, "", 0).Printf}
	if _, err := mirrorer.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if logged.Len() == 0 {
		t.Fatal("want a failing refresh to be reported somewhere")
	}
	if strings.Contains(logged.String(), upstreamToken) {
		t.Fatalf("the credential was written to the log: %s", logged.String())
	}

	// And the recorded failure reason, which the GUI shows, is clean too.
	failed, err := store.GetMirror(ctx, imported.ID)
	if err != nil {
		t.Fatalf("get mirror after failure: %v", err)
	}
	if failed.LastError == "" {
		t.Fatal("want the failure recorded on the mirror")
	}
	if strings.Contains(failed.LastError, upstreamToken) {
		t.Fatalf("the credential is in last_error: %s", failed.LastError)
	}
}

// TestGitPlatformStartsTheMirrorer asserts the seam, not the component.
//
// A mirror that is never refreshed is this repository's signature defect: the
// feature is written, unit-tested, and started by nothing, and the symptom is
// silence — an imported repository that simply never changes, indistinguishable
// from an upstream nobody has pushed to. So this type-checks cmd/git-platform
// and insists on a call to (*gitops.Mirrorer).Run in it. Type information is
// what makes the assertion worth anything: a grep for "Mirrorer" would be
// satisfied by a comment and a grep for ".Run(" by any other type's Run.
func TestGitPlatformStartsTheMirrorer(t *testing.T) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:  mirrorRepoRoot(t),
	}
	pkgs, err := packages.Load(cfg, "github.com/novaforge/novaforge/cmd/git-platform")
	if err != nil {
		t.Fatalf("load cmd/git-platform: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("cmd/git-platform did not load")
	}
	started := false
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			t.Fatalf("load cmd/git-platform: %v", pkg.Errors[0])
		}
		for _, f := range pkg.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Run" {
					return true
				}
				s := pkg.TypesInfo.Selections[sel]
				if s == nil {
					return true
				}
				recv := s.Recv()
				if p, ok := recv.(*types.Pointer); ok {
					recv = p.Elem()
				}
				named, ok := types.Unalias(recv).(*types.Named)
				if !ok || named.Obj().Pkg() == nil {
					return true
				}
				if named.Obj().Name() == "Mirrorer" &&
					named.Obj().Pkg().Path() == "github.com/novaforge/novaforge/internal/gitops" {
					started = true
				}
				return true
			})
		}
	}
	if !started {
		t.Fatal("cmd/git-platform never calls (*gitops.Mirrorer).Run: no mirror would ever be refreshed")
	}
}

// mirrorRepoRoot locates the repository from this test file's own path, so the
// test does not depend on where `go test` was invoked from.
func mirrorRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func hasRef(refs []gitops.Ref, name string) bool {
	for _, r := range refs {
		if r.Name == name {
			return true
		}
	}
	return false
}
