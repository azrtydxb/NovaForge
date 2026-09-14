package edge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/edge"
)

// captureGates records the request the edge forwards, standing in for the
// gates service, whose own behaviour is tested against real git and
// PostgreSQL in internal/gates.
type captureGates struct {
	gatesv1.GatesServiceClient
	got *gatesv1.ProposeGateChangeRequest
}

func (c *captureGates) ProposeGateChange(_ context.Context, in *gatesv1.ProposeGateChangeRequest, _ ...grpc.CallOption) (*gatesv1.ProposeGateChangeResponse, error) {
	c.got = in
	return &gatesv1.ProposeGateChangeResponse{RunNumber: 12, Branch: "gates/documentation-abc123", Title: "Disable the documentation gate"}, nil
}

// TestProposeGateChangeCarriesAnExplicitFalse pins the seam most likely to be
// wrong here: "enabled": false is the whole point of a disable toggle, and a
// JSON decode into a plain bool cannot tell it from an absent field — which
// the service rightly refuses as a proposal that changes nothing.
func TestProposeGateChangeCarriesAnExplicitFalse(t *testing.T) {
	org := uuid.New()
	for _, tc := range []struct {
		body        string
		wantSet     bool
		wantEnabled bool
		wantParams  string
	}{
		{`{"enabled": false}`, true, false, ""},
		{`{"enabled": true}`, true, true, ""},
		{`{"params": {"min_coverage": 70}}`, false, false, `{"min_coverage":70}`},
	} {
		gates := &captureGates{}
		cfg := edge.Config{
			Identity: stubIdentity{subject: &identityv1.Subject{UserId: uuid.NewString(), OrgId: org.String(), ActorKind: "user"}},
			Git:      gitv1.NewGitServiceClient(nil),
			Gates:    gates,
		}
		cfg.Handlers = edge.Handlers(cfg)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/acme/repos/api/gates/documentation/proposals", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer nf_tok")
		rec := httptest.NewRecorder()
		edge.NewRouter(cfg).ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: status %d %s", tc.body, rec.Code, rec.Body.String())
		}
		if gates.got.GetRepo() != "api" || gates.got.GetGate() != "documentation" {
			t.Fatalf("%s: forwarded repo/gate = %q/%q", tc.body, gates.got.GetRepo(), gates.got.GetGate())
		}
		if (gates.got.Enabled != nil) != tc.wantSet || gates.got.GetEnabled() != tc.wantEnabled {
			t.Fatalf("%s: enabled set=%v value=%v, want set=%v value=%v", tc.body, gates.got.Enabled != nil, gates.got.GetEnabled(), tc.wantSet, tc.wantEnabled)
		}
		if gates.got.GetParamsJson() != tc.wantParams {
			t.Fatalf("%s: params_json = %q, want %q", tc.body, gates.got.GetParamsJson(), tc.wantParams)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["run_number"] != float64(12) || out["branch"] != "gates/documentation-abc123" {
			t.Fatalf("%s: response %s", tc.body, rec.Body.String())
		}
	}
}
