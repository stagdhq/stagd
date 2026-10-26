// Package cli defines the stagd command tree.
package cli

import (
	"context"
	"fmt"
	"runtime"

	"github.com/urfave/cli/v3"

	"github.com/stagdhq/stagd/internal/client/api"
)

// DefaultAPIURL is hosted Stagd's API; --api-url (STAGD_API_URL) points
// the CLI at another, such as a local server in development.
const DefaultAPIURL = "https://app.stagd.dev"

// New returns the root command.
func New(version string) *cli.Command {
	return &cli.Command{
		Name:    "stagd",
		Usage:   "live, seeded preview environments for every pull request",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "api-url",
				Usage:   "Stagd API base URL",
				Value:   DefaultAPIURL,
				Sources: cli.EnvVars("STAGD_API_URL"),
			},
			&cli.StringFlag{
				Name:    "token",
				Usage:   "session token; overrides the one stored by stagd login",
				Sources: cli.EnvVars("STAGD_TOKEN"),
			},
			&cli.BoolFlag{
				Name:  "json",
				Usage: "print JSON instead of tables",
			},
		},
		Commands: []*cli.Command{
			loginCommand(),
			logoutCommand(),
			whoamiCommand(),
			orgsCommand(),
			githubCommand(),
			connectionsCommand(),
			reposCommand(),
			computeCommand(),
			secretsCommand(),
			integrationsCommand(),
			activityCommand(),
			previewCommand(),
			validateCommand(),
			relayCommand(version),
			versionCommand(version),
			updateCommand(),
		},
	}
}

// clients builds API clients from the global flags and the stored sign-in.
func clients(cmd *cli.Command) (*api.Clients, error) {
	return api.New(api.Config{
		BaseURL:   cmd.String("api-url"),
		Token:     token(cmd),
		UserAgent: "stagd-cli/" + cmd.Root().Version,
	})
}

func versionCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "print the CLI version",
		Action: func(_ context.Context, cmd *cli.Command) error {
			_, err := fmt.Fprintf(cmd.Root().Writer, "stagd %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)

			return err
		},
	}
}
