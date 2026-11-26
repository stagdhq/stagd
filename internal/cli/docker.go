package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/stagdhq/stagd/internal/dockerup"
)

func dockerCommand() *cli.Command {
	return &cli.Command{
		Name:  "docker",
		Usage: "run a preview's plan on Docker: what Stagd runs on Namespace and Docker hosts",
		Commands: []*cli.Command{
			dockerUpCommand(),
		},
	}
}

func dockerUpCommand() *cli.Command {
	return &cli.Command{
		Name:      "up",
		Usage:     "bring a preview's plan up on this machine's Docker: build, pull and start it",
		ArgsUsage: "<plan.json>",
		Description: "The plan says what the preview runs: its services, builds, volumes and\n" +
			"ingresses, where the source is, and the names of the environment variables\n" +
			"holding its secrets. Stagd runs this on the instances it makes; run it\n" +
			"yourself to bring the same preview up on your own Docker, with your\n" +
			"checkout as the source:\n\n" +
			"  stagd docker up plan.json --source .\n\n" +
			"Builds run side by side, as many as there are CPUs, while images are\n" +
			"pulled. Where it stands goes to <dir>/state, the logs beside it:\n" +
			"build-<service>.log, pull-<service>.log, up.log.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: ".stagd", Usage: "where the state, the logs, the compose file and a fetched source go"},
			&cli.StringFlag{Name: "project", Value: "stagd-preview", Usage: "the compose project"},
			&cli.BoolFlag{Name: "shared", Usage: "a Docker host other previews share: publish nothing, and reach each service as <service>.<project> on the project's network"},
			&cli.StringFlag{Name: "source", Usage: "the source in a directory here, rather than fetched from the plan's link"},
			&cli.StringFlag{Name: "keep-registry", Usage: "a registry to keep built images in, logged in to with STAGD_KEPT_PASSWORD: a build kept for the commit is pulled back rather than built"},
			&cli.StringFlag{Name: "keep-username", Value: "token", Usage: "the username for --keep-registry"},
			&cli.StringSliceFlag{Name: "keep", Usage: "service=image: where a service's build is kept, under --keep-registry"},
			&cli.BoolFlag{Name: "rebuild", Usage: "build again rather than pull back what's kept, keeping what's built"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return fmt.Errorf("usage: stagd docker up <plan.json>")
			}

			plan, err := dockerup.Load(cmd.Args().First())

			if err != nil {
				return err
			}

			opts := dockerup.Options{
				Dir:     cmd.String("dir"),
				Project: cmd.String("project"),
				Shared:  cmd.Bool("shared"),
				Source:  cmd.String("source"),
				OnState: func(line string) { fmt.Fprintln(os.Stderr, line) },
			}

			if host := cmd.String("keep-registry"); host != "" {
				kept := &dockerup.Kept{Host: host, Username: cmd.String("keep-username"), Password: os.Getenv("STAGD_KEPT_PASSWORD"), Images: map[string]string{}, Rebuild: cmd.Bool("rebuild")}

				for _, k := range cmd.StringSlice("keep") {
					service, image, ok := strings.Cut(k, "=")

					if !ok || service == "" || image == "" {
						return fmt.Errorf("--keep %q: service=image", k)
					}

					kept.Images[service] = image
				}

				opts.Kept = kept
			}

			return dockerup.Up(ctx, plan, opts)
		},
	}
}
