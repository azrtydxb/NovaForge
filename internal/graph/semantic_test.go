package graph_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/ctxasm"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/semanticindex"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/work"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func semanticFixture(t *testing.T, org, repo uuid.UUID) semanticindex.Produced {
	t.Helper()
	for _, tool := range []string{"scip-go", "scip"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("NOVAFORGE_REQUIRE_SEMANTIC_TOOLS") == "1" {
				t.Fatal(err)
			}
			t.Skip(err)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := semanticindex.ProducerConfig{Image: "registry.invalid/fixture@sha256:" + strings.Repeat("a", 64), Architecture: "arm64", CPUMilli: 1000, MemoryMiB: 1024, StorageMiB: 1024, DeadlineSeconds: 60}
	manifest, err := config.ExecutionManifest()
	if err != nil {
		t.Fatal(err)
	}
	s := semanticindex.Snapshot{OrgID: org, RepoID: repo, Revision: strings.Repeat("a", 40), RootURI: "file://" + root, ExecutionDigest: graph.SourceDigest(manifest), Files: map[string][]byte{
		"go.mod":     []byte("module fixture.test/semantic\n\ngo 1.26\n"),
		"library.go": []byte("package fixture\nfunc Hello() int { return 1 }\n"),
		"caller.go":  []byte("package fixture\nfunc Caller() int { return Hello() }\n"),
	}}
	for p, b := range s.Files {
		if err := os.WriteFile(filepath.Join(root, p), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(argv ...string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v %s", argv[0], err, stderr.String())
		}
		return b
	}
	run("scip-go", "index", "--repository-remote=repository", "--module-version=snapshot")
	raw := run("scip", "print", "--json", filepath.Join(root, "index.scip"))
	d, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	result, err := semanticindex.ImportSCIP(context.Background(), s, semanticindex.RunEvidence{Revision: s.Revision, SnapshotDigest: d, ExecutionDigest: s.ExecutionDigest, Tool: "scip-go", ToolVersion: "0.2.7"}, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return semanticindex.Produced{Snapshot: s, Manifest: manifest, Results: []semanticindex.Result{result}}
}

func TestSemanticRealToolStorageRPCAndFences(t *testing.T) {
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	scope := scopedCtx(org, uuid.New())
	p := semanticFixture(t, org, repo)
	if err := store.ReplaceSemanticIndex(scope, p); err == nil {
		t.Fatal("unfenced semantic publication accepted")
	}
	ctx, release, err := store.LockIndex(scope, org, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSemanticIndex(ctx, p); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if err := store.ReplaceSemanticIndex(ctx, p); err == nil {
		t.Fatal("released semantic session accepted")
	}
	const secret = "semantic-rpc-fixture"
	server := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, secret)))
	graphv1.RegisterGraphServiceServer(server, graph.NewGRPCServer(store, nil, nil, nil, nil, nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := graphv1.NewGraphServiceClient(conn)
	token, err := svcauth.Mint(secret, "semantic-test", org, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	call := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	got, err := client.Dependents(call, &graphv1.DependentsRequest{RepoId: repo.String(), Symbol: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetNodes()) != 1 || got.GetNodes()[0].GetAttrs()["path"] != "caller.go" {
		t.Fatalf("real compiler reference lost through RPC: %+v", got)
	}
	// The importer knows the referencing file, not the enclosing caller symbol.
	// An empty successful symbol answer would falsely mean Caller has no deps.
	if _, err := client.Dependencies(call, &graphv1.DependenciesRequest{RepoId: repo.String(), Symbol: "Caller"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("semantic caller dependencies must be explicit unavailable, got %v", err)
	}
	ctx, release, err = store.LockIndex(scope, org, repo)
	if err != nil {
		t.Fatal(err)
	}
	original := p.Results[0].Revision
	p.Results[0].Revision = strings.Repeat("b", 40)
	if err := store.ReplaceSemanticIndex(ctx, p); err == nil {
		t.Fatal("wrong revision accepted")
	}
	p.Results[0].Revision = original
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.ReplaceSemanticIndex(cancelled, p); err == nil {
		t.Fatal("cancelled publication accepted")
	}
	var count int
	if err := store.Pool().QueryRow(scope, `SELECT count(*) FROM graph.semantic_snapshots WHERE org_id=$1 AND repo_id=$2 AND NOT absence_safe`, org, repo).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed publish lost prior evidence: %d %v", count, err)
	}
	release()
	if err := store.PurgeRepository(scope, repo); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LockIndex(scope, org, repo); err == nil {
		t.Fatal("deleted scope admitted")
	}
	if err := store.Pool().QueryRow(scope, `SELECT count(*) FROM graph.semantic_snapshots WHERE org_id=$1 AND repo_id=$2`, org, repo).Scan(&count); err != nil || count != 0 {
		t.Fatalf("semantic evidence survived purge: %d %v", count, err)
	}
}

func TestSemanticGenerationInvalidatedBeforeRefresh(t *testing.T) {
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	scope := scopedCtx(org, uuid.New())
	p := semanticFixture(t, org, repo)
	ctx, release, err := store.LockIndex(scope, org, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := store.ReplaceSemanticIndex(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := store.SetIndexCheckpoint(ctx, org, repo, "", "new-extractor"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.Pool().QueryRow(scope, `SELECT count(*) FROM graph.semantic_snapshots WHERE org_id=$1 AND repo_id=$2`, org, repo).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("old semantic snapshot still published during partial refresh")
	}
	if err := store.Pool().QueryRow(scope, `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND attrs->>'semantic'='true'`, org, repo).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("old semantic nodes still answer during partial refresh")
	}
}

func TestDeclaredRelationshipsNeverOperationalProof(t *testing.T) {
	for _, body := range []string{
		`{"schema":1,"entities":[{"key":"svc","kind":"service","name":"API"},{"key":"release","kind":"deployment","name":"Release"}],"edges":[{"from":"svc","to":"release","kind":"deployed_as"}]}`,
		`{"schema":1,"entities":[],"edges":[{"from":"foreign-uuid","to":"owner","kind":"owned_by"}]}`,
	} {
		if _, err := graph.ParseRelationshipManifest([]byte(body)); err == nil {
			t.Fatal("invalid declared authority accepted")
		}
	}
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	ctx := scopedCtx(org, uuid.New())
	body := []byte(`{"schema":1,"entities":[{"key":"svc","kind":"service","name":"API"},{"key":"team","kind":"owner","name":"Identity Team"}],"edges":[{"from":"svc","to":"team","kind":"owned_by"}]}`)
	if err := store.ReplaceDeclaredRelationships(ctx, org, repo, strings.Repeat("a", 40), body); err != nil {
		t.Fatal(err)
	}
	var attrs []byte
	if err := store.Pool().QueryRow(ctx, `SELECT attrs FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND key=$3`, org, repo, repo.String()+":declared:svc").Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal(attrs, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["provenance"] != "declared" || fields["absence_safe"] != "false" {
		t.Fatal("declaration masquerades as observation")
	}
	if err := store.ReplaceDeclaredRelationships(scopedCtx(uuid.New(), uuid.New()), org, repo, strings.Repeat("a", 40), body); err == nil {
		t.Fatal("foreign declaration write accepted")
	}
	if err := store.ReplaceDeclaredRelationships(ctx, org, repo, strings.Repeat("b", 40), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticRealToolSymbolsReachContext(t *testing.T) {
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	scope := scopedCtx(org, uuid.New())
	p := semanticFixture(t, org, repo)
	ctx, release, err := store.LockIndex(scope, org, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSemanticIndex(ctx, p); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	bundle, err := ctxasm.Assemble(scope, ctxasm.Input{OrgID: org, RepoID: repo, Graph: store, WorkItem: work.Item{Goal: "Hello Caller"}, TokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, snippet := range bundle.Files {
		if snippet.Signal == "symbol" {
			found[snippet.Text] = true
		}
	}
	if !found["Hello"] || !found["Caller"] {
		t.Fatalf("NULL semantic signatures truncated context symbols: %+v", bundle.Files)
	}
}
