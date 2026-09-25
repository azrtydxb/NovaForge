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
		// Provider/Helm output can contain secrets. Public evidence is emitted only
		// by RunHelmJob after its fixed contract has validated it.
		os.Stderr.WriteString("deployment executor could not establish verified delivery evidence\n")
		os.Exit(1)
	}
}
