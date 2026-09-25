package knowledge_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/knowledge"
)

func TestSupersedeScopeHistoryAndConcurrency(t *testing.T) {
	s := newStore(t)
	org, repo := uuid.New(), uuid.New()
	ctx := scopedCtx(org, uuid.New())
	record := func(o, r uuid.UUID) knowledge.Entry {
		t.Helper()
		e, err := s.Record(scopedCtx(o, uuid.New()), knowledge.Entry{OrgID: o, RepoID: r, Key: uniqueKey("correction"), Kind: "correction", Title: "correction", Body: "original evidence"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	a, b, c := record(org, repo), record(org, repo), record(org, repo)
	foreign, otherRepo := record(uuid.New(), repo), record(org, uuid.New())
	for _, id := range []uuid.UUID{a.ID, foreign.ID, otherRepo.ID, uuid.New()} {
		if err := s.Supersede(ctx, a.ID, id); err == nil {
			t.Errorf("accepted invalid replacement %s", id)
		}
	}
	if err := s.Supersede(ctx, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Supersede(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if err := s.Supersede(ctx, a.ID, c.ID); err == nil {
		t.Error("retargeted immutable history")
	}
	if err := s.Supersede(ctx, b.ID, a.ID); err == nil {
		t.Error("accepted cycle")
	}
	if err := s.Supersede(ctx, b.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Supersede(ctx, c.ID, a.ID); err == nil {
		t.Error("accepted longer cycle")
	}
	old, err := s.Get(ctx, a.ID)
	if err != nil || old.Body != a.Body || old.SupersededBy == nil || *old.SupersededBy != b.ID {
		t.Fatalf("lost history: %+v %v", old, err)
	}
	// Re-recording an old key must not rewrite superseded evidence.
	a.Body = "rewritten history"
	if _, err := s.Record(ctx, a, nil); err == nil {
		t.Fatal("superseded history overwritten")
	}
	old, err = s.Get(ctx, a.ID)
	if err != nil || old.Body != "original evidence" {
		t.Fatalf("history changed: %+v %v", old, err)
	}
	current, err := s.SearchText(ctx, org, repo, "correction", 10)
	if err != nil || len(current) != 1 || current[0].ID != c.ID {
		t.Fatalf("current search: %+v %v", current, err)
	}
	// Both transactions must not win the opposite-edge race.
	x, y := record(org, repo), record(org, repo)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, pair := range [][2]uuid.UUID{{x.ID, y.ID}, {y.ID, x.ID}} {
		wg.Add(1)
		go func(p [2]uuid.UUID) { defer wg.Done(); <-start; results <- s.Supersede(ctx, p[0], p[1]) }(pair)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("opposite edges succeeded %d times", successes)
	}
}
