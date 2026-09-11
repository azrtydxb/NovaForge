package agents_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/agents"
)

func TestWallclockExceeded(t *testing.T) {
	b := agents.NewBudget(10*time.Millisecond, 1000, 1000)
	time.Sleep(30 * time.Millisecond)

	err := b.Check()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, agents.ErrOverBudget) {
		t.Fatalf("want errors.Is(err, agents.ErrOverBudget), got %v", err)
	}
	if !strings.Contains(err.Error(), "wallclock") {
		t.Fatalf("want error containing %q, got %q", "wallclock", err.Error())
	}
}

func TestTokenLimitExceeded(t *testing.T) {
	b := agents.NewBudget(time.Hour, 100, 1_000_000)
	b.AddTokens(101)

	err := b.Check()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, agents.ErrOverBudget) {
		t.Fatalf("want errors.Is(err, agents.ErrOverBudget), got %v", err)
	}
	if !strings.Contains(err.Error(), "tokens") {
		t.Fatalf("want error containing %q, got %q", "tokens", err.Error())
	}
}

func TestCostLimitExceeded(t *testing.T) {
	b := agents.NewBudget(time.Hour, 1_000_000, 100)
	b.AddCostMicros(101)

	err := b.Check()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, agents.ErrOverBudget) {
		t.Fatalf("want errors.Is(err, agents.ErrOverBudget), got %v", err)
	}
	if !strings.Contains(err.Error(), "cost") {
		t.Fatalf("want error containing %q, got %q", "cost", err.Error())
	}
}

func TestUnderBudgetPasses(t *testing.T) {
	b := agents.NewBudget(time.Hour, 1000, 1000)
	b.AddTokens(10)
	b.AddCostMicros(10)

	if err := b.Check(); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestBudgetConcurrentToolCallsAreSafe(t *testing.T) {
	b := agents.NewBudget(time.Hour, 1_000_000, 1_000_000)
	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() {
			b.AddTokens(10)
			b.AddCostMicros(10)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}
	if err := b.Check(); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}
