package cli

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/stagdhq/stagd/internal/relay"
)

func relayCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "relay",
		Usage: "connect compute Stagd can't reach to Stagd: run it next to the compute",
		Description: "The relay dials out to Stagd and holds the connection; Stagd manages the\n" +
			"compute and sends preview traffic through it. Nothing next to the relay\n" +
			"needs to be reachable from outside.\n\n" +
			"In a Kubernetes pod it fronts the cluster, as the pod's service account\n" +
			"(see deploy/kubernetes/stagd-relay.yaml), and only connects Stagd to\n" +
			"services in stagd-* namespaces.\n\n" +
			"In a container with the Docker socket, it fronts that Docker host:\n\n" +
			"  docker run -d --name stagd-relay --restart unless-stopped \\\n" +
			"    -v /var/run/docker.sock:/var/run/docker.sock \\\n" +
			"    -e STAGD_URL=https://stagd.example.com -e STAGD_RELAY_TOKEN=stagd_relay.... \\\n" +
			"    ghcr.io/stagdhq/stagd:latest relay\n\n" +
			"Whoever reaches the Docker API controls the host: use a host meant for previews.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "url",
				Usage:    "Stagd's address, e.g. https://stagd.example.com",
				Sources:  cli.EnvVars("STAGD_URL"),
				Required: true,
			},
			&cli.StringFlag{
				Name:     "token",
				Usage:    "the compute connection's relay token",
				Sources:  cli.EnvVars("STAGD_RELAY_TOKEN"),
				Required: true,
			},
			&cli.StringFlag{
				Name:    "http",
				Usage:   "an address, e.g. :8081, to take preview traffic from a Cloudflare Tunnel next to the relay (deploy/kubernetes/stagd-tunnel.yaml); empty takes none",
				Sources: cli.EnvVars("STAGD_RELAY_HTTP"),
			},
			&cli.StringFlag{
				Name:    "log-format",
				Usage:   "text or json",
				Value:   "text",
				Sources: cli.EnvVars("STAGD_LOG_FORMAT"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			r := &relay.Relay{
				URL:     cmd.String("url"),
				Token:   cmd.String("token"),
				Version: version,
				Log:     newLogger(cmd.String("log-format")),
			}

			switch {
			case relay.InCluster():
				api, err := relay.KubeAPI()
				if err != nil {
					return err
				}

				r.Kind, r.API, r.Allow = "kubernetes", api, relay.AllowKube

			case relay.HasDocker():
				r.Kind, r.API, r.Allow = "docker", relay.DockerAPI(), relay.DockerAllow(relay.OwnContainer())

			default:
				return cli.Exit("stagd relay runs in a Kubernetes pod, or in a container with the Docker socket at "+relay.DockerSocket, 2)
			}

			r.Log.Info("relay: starting", "kind", r.Kind, "stagd", r.URL, "version", version)

			// Preview traffic from the tunnel, beside the connection to Stagd.
			if addr := cmd.String("http"); addr != "" {
				srv := &http.Server{Addr: addr, Handler: r.HTTP(), ReadHeaderTimeout: 30 * time.Second}

				go func() {
					if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
						r.Log.Error("relay: tunnel traffic", "addr", addr, "err", err)
					}
				}()

				defer srv.Close()

				r.Log.Info("relay: taking tunnel traffic", "addr", addr)
			}

			return r.Run(ctx)
		},
	}
}

// newLogger writes the relay's logs to stderr, as text or JSON.
func newLogger(format string) *slog.Logger {
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}

	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}
