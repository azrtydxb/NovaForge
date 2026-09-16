package maintenance_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Produce actual tests-gate evaluations; never seed invented coverage values.
func coverageHistory(t *testing.T, org, repo uuid.UUID) gatesv1.GatesServiceClient {
	t.Helper()
	url := proposeDBURL(t)
	if err := database.Migrate(url, "gates", gates.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM gates.gate_evaluations WHERE org_id=$1 AND repo_id=$2", org, repo)
	})
	store := gates.NewStore(pool)
	dir := t.TempDir()
	for file, body := range map[string]string{
		"go.mod":  "module example.com/coverage\ngo 1.24\n",
		"calc.go": "package coverage\nfunc Add() int { return 1 }; func Sub() int { return 2 }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i, calls := range []string{"Add(); Sub()", "Add()"} {
		body := "package coverage\nimport \"testing\"\nfunc TestCalc(t *testing.T) { " + calls + " }\n"
		if err := os.WriteFile(filepath.Join(dir, "calc_test.go"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		eval, err := gates.Runners["tests"](context.Background(), gates.Input{
			OrgID: org, RepoID: repo, RunID: uuid.New(), TargetSHA: fmt.Sprintf("%040x", i+1), WorkDir: dir, Exec: analysis.DefaultExec,
			Params: map[string]any{"minimum_coverage": 80},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RecordEvaluation(proposeScopedCtx(org), eval); err != nil {
			t.Fatal(err)
		}
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, sweepSecret)))
	gatesv1.RegisterGatesServiceServer(srv, gates.NewGRPCServer(&gates.Controller{Store: store}, nil, nil, nil))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return gatesv1.NewGatesServiceClient(conn)
}
