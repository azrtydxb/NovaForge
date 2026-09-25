package reviews_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/reviews"
	"google.golang.org/grpc"
)

type pinnedGateResponse struct {
	gatesv1.GatesServiceClient
	response *gatesv1.MayMergeResponse
	request  *gatesv1.MayMergeRequest
}

func (g *pinnedGateResponse) MayMerge(_ context.Context, r *gatesv1.MayMergeRequest, _ ...grpc.CallOption) (*gatesv1.MayMergeResponse, error) {
	g.request = r
	return g.response, nil
}
func TestGatesClientRefusesUnboundAndMismatchedAuthority(t *testing.T) {
	const source = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const target = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tc := range []struct {
		name, source, target string
		allowed              bool
	}{
		{"legacy empty identity", "", "", false},
		{"wrong source", target, target, false},
		{"wrong target", source, source, false},
		{"exact pair", source, target, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &pinnedGateResponse{response: &gatesv1.MayMergeResponse{Allowed: true, EvaluatedSourceSha: tc.source, EvaluatedTargetSha: tc.target}}
			client := reviews.GatesClient{Gates: g}
			id := uuid.New()
			allowed, _, err := client.MayMergePinned(context.Background(), id, source, target)
			if allowed != tc.allowed || (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", allowed, err)
			}
			if g.request.GetRunId() != id.String() || g.request.GetExpectedSourceSha() != source || g.request.GetExpectedTargetSha() != target {
				t.Fatalf("lost pinned request: %v", g.request)
			}
		})
	}
}
