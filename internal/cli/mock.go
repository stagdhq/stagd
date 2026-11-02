package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/stagdhq/stagd/internal/mock"
)

func mockCommand() *cli.Command {
	return &cli.Command{
		Name:  "mock",
		Usage: "answer email and SMS vendors' APIs locally, passing mail on to an SMTP inbox",
		Description: "Stands in for the vendors an app sends email and text messages through, so nothing\n" +
			"leaves: emails go on to an SMTP server such as Mailpit, text messages are kept\n" +
			"(GET /texts). Stagd runs it in previews with mail capture, as stagd-mock; it works\n" +
			"on your machine as well. Point the vendors' SDKs at it:\n\n" +
			"  Resend      RESEND_BASE_URL=http://localhost:8080/resend (RESEND_API_URL in Python)\n" +
			"  Amazon SES  AWS_ENDPOINT_URL_SES=http://localhost:4566 (and _SESV2)\n" +
			"  Amazon SNS  AWS_ENDPOINT_URL_SNS=http://localhost:4566 (text messages)\n\n" +
			"  stagd mock --smtp localhost:1025",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "listen",
				Usage:   "where the vendors with JSON APIs (Resend) are answered, under /<vendor>",
				Value:   ":8080",
				Sources: cli.EnvVars("STAGD_MOCK_LISTEN"),
			},
			&cli.StringFlag{
				Name:    "aws-listen",
				Usage:   "where Amazon SES and SNS are answered, at the root as the AWS SDKs want",
				Value:   ":4566",
				Sources: cli.EnvVars("STAGD_MOCK_AWS_LISTEN"),
			},
			&cli.StringFlag{
				Name:    "smtp",
				Usage:   "the SMTP server emails go on to, host:port (Mailpit's is :1025); empty only logs them",
				Sources: cli.EnvVars("STAGD_MOCK_SMTP"),
			},
			&cli.StringFlag{
				Name:    "log-format",
				Usage:   "text or json",
				Value:   "text",
				Sources: cli.EnvVars("STAGD_LOG_FORMAT"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := newLogger(cmd.String("log-format"))
			s := &mock.Server{SMTP: cmd.String("smtp"), Log: log}

			servers := []*http.Server{
				{Addr: cmd.String("listen"), Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second},
				{Addr: cmd.String("aws-listen"), Handler: s.AWSHandler(), ReadHeaderTimeout: 10 * time.Second},
			}

			failed := make(chan error, len(servers))

			for _, srv := range servers {
				listener, err := net.Listen("tcp", srv.Addr)
				if err != nil {
					return fmt.Errorf("listening on %s: %w", srv.Addr, err)
				}

				go func() {
					if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
						failed <- err
					}
				}()
			}

			log.Info("stagd mock: answering", "vendors", cmd.String("listen"), "aws", cmd.String("aws-listen"), "smtp", cmd.String("smtp"))

			select {
			case <-ctx.Done():
			case err := <-failed:
				return err
			}

			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			for _, srv := range servers {
				_ = srv.Shutdown(shutdown)
			}

			return nil
		},
	}
}
