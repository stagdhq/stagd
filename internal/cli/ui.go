package cli

import (
	"context"
	"net/url"

	tea "charm.land/bubbletea/v2"
	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/stagdhq/stagd/internal/client/api"
	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/tui"
)

func uiCommand() *cli.Command {
	return &cli.Command{
		Name:  "ui",
		Usage: "the org's previews in the terminal: follow, describe, logs",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "demo", Usage: "made-up previews, to try it without an org"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			var src tui.Source
			c := tui.Context{Server: host(cmd.String("api-url")), Version: cmd.Root().Version}

			if cmd.Bool("demo") {
				src = tui.NewDemo()
				c.Org, c.User, c.Server = "acme", "ada@acme.example", "demo"
			} else {
				t := token(cmd)

				if t == "" {
					return errNotSignedIn
				}

				me, err := getMe(ctx, cmd, t)
				if err != nil {
					return err
				}

				cl, err := clients(cmd)
				if err != nil {
					return err
				}

				src = apiSource{cl}
				c.Org, c.User = me.GetActive().GetOrg().GetSlug(), me.GetUser().GetEmail()
			}

			_, err := tea.NewProgram(tui.New(src, c)).Run()

			return err
		},
	}
}

func host(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}

	return u
}

// apiSource is the UI's previews from Stagd's API, as the signed-in person.
type apiSource struct{ c *api.Clients }

func (s apiSource) Previews(ctx context.Context) ([]*apiv1.Preview, error) {
	res, err := s.c.Previews.ListPreviews(ctx, connect.NewRequest(&apiv1.ListPreviewsRequest{PageSize: maxPreviewPage}))
	if err != nil {
		return nil, err
	}

	return res.Msg.GetPreviews(), nil
}

func (s apiSource) Preview(ctx context.Context, id string) (*apiv1.Preview, error) {
	res, err := s.c.Previews.GetPreview(ctx, connect.NewRequest(&apiv1.GetPreviewRequest{Id: id}))
	if err != nil {
		return nil, err
	}

	return res.Msg.GetPreview(), nil
}

func (s apiSource) Logs(ctx context.Context, id string, since *timestamppb.Timestamp) ([]*apiv1.LogLine, error) {
	res, err := s.c.Previews.ListLogs(ctx, connect.NewRequest(&apiv1.ListLogsRequest{PreviewId: id, Since: since, Limit: 500}))
	if err != nil {
		return nil, err
	}

	return res.Msg.GetLines(), nil
}

func (s apiSource) Wake(ctx context.Context, id string) error {
	_, err := s.c.Previews.WakePreview(ctx, connect.NewRequest(&apiv1.WakePreviewRequest{Id: id}))

	return err
}

func (s apiSource) Destroy(ctx context.Context, id string) error {
	_, err := s.c.Previews.DestroyPreview(ctx, connect.NewRequest(&apiv1.DestroyPreviewRequest{Id: id}))

	return err
}
