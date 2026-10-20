// Command stagd is the Stagd CLI: a client of the Stagd API, for people, CI
// and coding agents.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"stagd.dev/stagd/internal/cli"
)

// Set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.New(version).Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "stagd:", err)
		os.Exit(1)
	}
}
