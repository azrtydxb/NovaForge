package agents

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// ErrOverBudget is wrapped by Check's error when any dimension of a Budget
// has been exceeded.
var ErrOverBudget = errors.New("over budget")

// Budget tracks the wall-clock, token, and cost limits of one agent run.
// Every field that can be mutated by concurrent tool calls is an atomic, so
// AddTokens and AddCostMicros are safe to call from multiple goroutines at
// once without a lock.
type Budget struct {
	started    time.Time
	wallclock  time.Duration
	tokenLimit int64
	costLimit  int64

	tokensUsed atomic.Int64
	costUsed   atomic.Int64
}

// NewBudget builds a Budget whose wall-clock window starts now.
func NewBudget(wallclock time.Duration, tokens int64, costMicros int64) *Budget {
	return &Budget{
		started:    time.Now(),
		wallclock:  wallclock,
		tokenLimit: tokens,
		costLimit:  costMicros,
	}
}

// AddTokens atomically accounts n more tokens against the budget.
func (b *Budget) AddTokens(n int64) {
	b.tokensUsed.Add(n)
}

// AddCostMicros atomically accounts n more micros of cost against the
// budget.
func (b *Budget) AddCostMicros(n int64) {
	b.costUsed.Add(n)
}

// Check reports whether any dimension of the budget has been exceeded. The
// wall-clock dimension is derived from the current time rather than an
// atomic counter, since it advances on its own.
func (b *Budget) Check() error {
	if elapsed := time.Since(b.started); elapsed > b.wallclock {
		return fmt.Errorf("%w: wallclock limit exceeded (%s > %s)", ErrOverBudget, elapsed, b.wallclock)
	}
	if used := b.tokensUsed.Load(); used > b.tokenLimit {
		return fmt.Errorf("%w: tokens limit exceeded (%d > %d)", ErrOverBudget, used, b.tokenLimit)
	}
	if used := b.costUsed.Load(); used > b.costLimit {
		return fmt.Errorf("%w: cost limit exceeded (%d > %d micros)", ErrOverBudget, used, b.costLimit)
	}
	return nil
}
