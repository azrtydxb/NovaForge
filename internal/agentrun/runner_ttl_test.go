package agentrun_test

import (
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// TestRunCredentialOutlivesTheRun pins that a run's credential lasts as long
// as the run may. It was minted once with the five-minute worker TTL, so every
// service call a run made after its fifth minute was refused.
func TestRunCredentialOutlivesTheRun(t *testing.T) {
	limited := agents.Run{WallclockLimit: 40 * time.Minute}
	if got := agentrun.RunCredentialTTL(limited); got <= limited.WallclockLimit {
		t.Fatalf("credential TTL %v does not outlast the run's %v wall-clock limit", got, limited.WallclockLimit)
	}
	if got := agentrun.RunCredentialTTL(agents.Run{}); got <= svcauth.DefaultTTL {
		t.Fatalf("an unlimited run's credential lives %v, no longer than a worker token", got)
	}
}
