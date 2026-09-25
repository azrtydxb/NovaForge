package reviews_test

import (
	"context"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/svcauth"
)

func proofServiceContext(t *testing.T, ctx context.Context, name string) context.Context {
	t.Helper()
	scope, err := authz.FromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := svcauth.Mint("proof-test-key", name, scope.OrgID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := svcauth.ScopeFromToken("proof-test-key", token)
	if err != nil {
		t.Fatal(err)
	}
	return authz.WithScope(ctx, verified)
}

func TestProofAuthorityCannotBeForgedOrCrossOrganization(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run, err := store.CreateRun(ctx, reviews.Run{OrgID: org, RepoID: uuid.New(), Title: "proof authority", SourceRef: "feature", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	gates := proofServiceContext(t, ctx, "gates")
	if err := store.RecordProof(gates, run.ID, "tests", "fail", "actual gate failure"); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{"tests", "approval/deploy", "review:reviewer", "agent-blocked"} {
		if err := store.RecordProof(ctx, run.ID, gate, "pass", "forged"); err == nil {
			t.Errorf("human forged reserved proof %q", gate)
		}
	}
	if err := store.RecordProof(proofServiceContext(t, scopedCtx(uuid.New()), "gates"), run.ID, "tests", "pass", "foreign"); err == nil {
		t.Error("foreign org overwrote proof")
	}
	if err := store.RecordProof(proofServiceContext(t, ctx, "work-reviews"), run.ID, "tests", "pass", "wrong producer"); err == nil {
		t.Error("review service overwrote gate")
	}
	if err := store.RecordProof(gates, run.ID, "arbitrary-human-name", "pass", "unknown gate"); err == nil {
		t.Error("unknown gate accepted")
	}
	scope, _ := authz.FromContext(ctx)
	assertion := "assertion/" + scope.ActorID.String() + "/manual-test"
	if err := store.RecordProof(ctx, run.ID, assertion, "pass", "human report"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProof(scopedCtx(org), run.ID, assertion, "pass", "another human"); err == nil {
		t.Error("another human overwrote assertion")
	}
	rows, err := store.ListProof(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rows {
		if p.Gate == "tests" && (p.Status != "fail" || p.Detail != "actual gate failure") {
			t.Errorf("gate overwritten: %+v", p)
		}
	}
	if _, err := store.ListProof(scopedCtx(uuid.New()), run.ID); err == nil {
		t.Error("foreign org read proof")
	}
}

func TestProofAuditRetainsReplacedEvidenceAndLegacyIsUnverified(t *testing.T) {
	pool := storePool(t)
	store := reviews.NewStore(pool)
	ctx := scopedCtx(uuid.New())
	scope, _ := authz.FromContext(ctx)
	run, err := store.CreateRun(ctx, reviews.Run{OrgID: scope.OrgID, RepoID: uuid.New(), Title: "proof audit", SourceRef: "feature", TargetRef: "main", AuthorID: scope.ActorID, AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO reviews.run_proof(run_id,gate,status,detail) VALUES($1,'security','pass','legacy claim')`, run.ID); err != nil {
		t.Fatal(err)
	}
	server := reviews.NewGRPCServer(store)
	before, err := server.ListProof(ctx, &reviewsv1.ListProofRequest{RunId: run.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Proof) != 1 || before.Proof[0].Producer != "" {
		t.Fatalf("legacy gained invented authority: %v", before)
	}
	gates := proofServiceContext(t, ctx, "gates")
	for _, state := range []string{"fail", "pass"} {
		if err = store.RecordProof(gates, run.ID, "tests", state, "actual evidence "+state); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM reviews.proof_audit a JOIN reviews.runs r ON r.id=a.run_id WHERE r.org_id=$1 AND a.run_id=$2 AND a.producer='gates' AND a.gate='tests'`, scope.OrgID, run.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	out, err := server.ListProof(ctx, &reviewsv1.ListProofRequest{RunId: run.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range out.Proof {
		if p.Gate == "tests" && (p.Producer != "gates" || p.Status != "pass") {
			t.Fatalf("missing current provenance: %v", p)
		}
	}
}

func TestProofRPCRequiresAuthenticatedProducer(t *testing.T) {
	store := newStore(t)
	ctx := scopedCtx(uuid.New())
	scope, _ := authz.FromContext(ctx)
	run, err := store.CreateRun(ctx, reviews.Run{OrgID: scope.OrgID, RepoID: uuid.New(), Title: "RPC proof", SourceRef: "feature", TargetRef: "main", AuthorID: scope.ActorID, AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, "proof-rpc-key")))
	reviewsv1.RegisterReviewsServiceServer(server, reviews.NewGRPCServer(store))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := reviewsv1.NewReviewsServiceClient(conn)
	request := &reviewsv1.RecordProofRequest{RunId: run.ID.String(), Gate: "tests", Status: "pass", Detail: "verified producer"}
	for _, producer := range []string{"gates", "work-reviews", "agent-runtime", "edge"} {
		token, err := svcauth.Mint("proof-rpc-key", producer, scope.OrgID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		callctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
		_, err = client.RecordProof(callctx, request)
		if producer == "gates" {
			if err != nil {
				t.Fatal(err)
			}
		} else if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("producer %s: %v", producer, err)
		}
	}
	if _, err = client.RecordProof(context.Background(), request); err == nil {
		t.Fatal("unauthenticated proof accepted")
	}
}
