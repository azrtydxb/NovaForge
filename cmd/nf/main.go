// Command nf is the NovaForge command-line client. While the web GUI is
// deferred, nf is the human entry point to the platform.
package main

import (
	"os"

	"github.com/novaforge/novaforge/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
