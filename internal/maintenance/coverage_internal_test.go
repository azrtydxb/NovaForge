package maintenance

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
)

func TestCoverageEvidenceNeverGuesses(t *testing.T) {
	repo := uuid.New()
	if _, err := gateCoverage(context.Background(), nil, repo); err == nil {
		t.Fatal("unconfigured gates reported clean history")
	}
	if _, err := coverageEvidence(nil, repo); err == nil {
		t.Fatal("missing history accepted")
	}
	for _, tc := range []struct {
		name         string
		value        *float64
		status, repo string
		valid        bool
	}{
		{"measured zero", floatPtr(0), "pass", repo.String(), true},
		{"below gate minimum", floatPtr(50), "fail", repo.String(), true},
		{"missing", nil, "pass", repo.String(), false},
		{"failed measurement", nil, "error", repo.String(), false},
		{"wrong repository", floatPtr(50), "pass", uuid.NewString(), false},
		{"nonfinite", floatPtr(math.NaN()), "pass", repo.String(), false},
		{"out of range", floatPtr(101), "pass", repo.String(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			latest := &gatesv1.Evaluation{Id: uuid.NewString(), RepoId: tc.repo, TargetSha: "latest", Gate: "tests", Status: tc.status, CoveragePercent: tc.value}
			previous := &gatesv1.Evaluation{Id: uuid.NewString(), RepoId: repo.String(), TargetSha: "previous", Gate: "tests", Status: "pass", CoveragePercent: floatPtr(100)}
			sample, err := coverageEvidence([]*gatesv1.Evaluation{latest, previous}, repo)
			if (err == nil) != tc.valid {
				t.Fatalf("sample=%+v err=%v", sample, err)
			}
			if tc.valid && (sample.Latest == nil || *sample.Latest != *tc.value || sample.Previous == nil || *sample.Previous != 100) {
				t.Fatalf("lost measurements: %+v", sample)
			}
		})
	}
}

func floatPtr(v float64) *float64 { return &v }
