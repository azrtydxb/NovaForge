package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/service"
)

type startupGit struct{ gitv1.GitServiceClient }

func TestIndependentReviewStartupRequiresExplicitManagedOwner(t *testing.T) {
	for _, owner := range []string{"", "https://owner.example/v1"} {
		t.Run(owner, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "review.json")
			raw := `{"execution_owner_url":"` + owner + `","wallclock_seconds":30,"max_input_bytes":4096,"max_output_tokens":1024,"max_concurrent_requests":1,"max_roles":1}`
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			server := reviews.NewGRPCServer(reviews.NewStore(nil))
			server.Git = startupGit{}
			// Deliberately managed-shaped: choosing the general inference URL
			// would pass URL validation and fail the separate nil-store check.
			// No datastore or network request is needed for this refusal test.
			cfg := service.Config{ReviewConfigFile: path, AgentsAddr: "127.0.0.1:1", HMACSecret: "test", AIEndpoint: "https://ordinary.example/executions/v1", AIModel: "test", AIAPIKey: "test"}
			cleanup, err := startIndependentReviews(context.Background(), cfg, server)
			if cleanup != nil {
				cleanup()
			}
			if err == nil || !strings.Contains(err.Error(), "explicit HTTPS managed execution owner required") || server.ReviewWorker != nil {
				t.Fatalf("unqualified owner activated or used fallback: err=%v worker=%v", err, server.ReviewWorker)
			}
		})
	}
}
