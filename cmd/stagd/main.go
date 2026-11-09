// Command stagd is the Stagd CLI: a client of the Stagd API, for people, CI
// and coding agents.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/stagdhq/stagd/internal/cli"
)

// Set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := cli.New(version)

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "stagd:", err)

		if hint := cli.StatusHint(err, cmd.String("api-url")); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}

		os.Exit(1)
	}
}
