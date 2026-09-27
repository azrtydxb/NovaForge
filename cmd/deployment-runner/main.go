// Command deployment-runner executes the operator-bundled Helm chart. It is an
// isolated Job entrypoint, not an API or a general repository command runner.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/novaforge/novaforge/internal/deployment"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := deployment.RunHelmJob(ctx, os.Args[1:], os.Stdout); err != nil {
		// Pod logs combine stdout and stderr. RunHelmJob may already have emitted
		// a verified failed-release JSON record; appending prose would invalidate
		// that evidence and turn every definitive failure into an unknown result.
		// An exit failure without a record remains uncertain to the controller.
		os.Exit(1)
	}
}
