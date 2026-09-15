package main

import (
	"context"
	"time"

	"github.com/novaforge/novaforge/internal/maintenance"
)

// firstSweepDelay lets the service's peers come up before the first sweep
// reads from them.
const firstSweepDelay = 2 * time.Minute

// runMaintenanceScanners sweeps every repository on an interval, proposing a
// Work Item for each finding. Nothing executes a fix and nothing starts an
// agent: a proposal is a plain, unassigned Work Item a person decides about.
// The sweep itself is maintenance.Sweeper, where a test can reach it.
func runMaintenanceScanners(ctx context.Context, sweeper *maintenance.Sweeper, every time.Duration) {
	sweeper.Run(ctx, firstSweepDelay, every)
}
