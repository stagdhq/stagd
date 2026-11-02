package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	"github.com/stagdhq/stagd/internal/client/api"
	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func previewTextsCommand() *cli.Command {
	return &cli.Command{
		Name:      "texts",
		Usage:     "read the text messages a preview's app sent (stagd.yaml services: { sms: {} })",
		ArgsUsage: "<id>",
		Description: "Lists the text messages the app sent through Amazon SNS, newest first: caught by Stagd,\n" +
			"never sent to a phone.\n\n" +
			"--wait waits for the next one (to a number containing --to, when given) and prints its text,\n" +
			"so a script can sign in to the preview with the code it's sent:\n\n" +
			"   stagd preview texts <id> --wait --to 4791234567\n\n" +
			"It exits non-zero when nothing arrives before --timeout.",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "limit", Usage: "list at most this many", Value: 50},
			&cli.BoolFlag{Name: "wait", Usage: "wait for the next text message to arrive, then print it"},
			&cli.StringFlag{Name: "to", Usage: "with --wait: only one to a number containing this"},
			&cli.DurationFlag{Name: "timeout", Usage: "with --wait: give up after this long", Value: 2 * time.Minute},
		},
		Action: previewTexts,
	}
}

func previewTexts(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().Get(0)

	if id == "" {
		return cli.Exit("usage: stagd preview texts <id>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("wait") {
		t, err := waitForText(ctx, cmd, c, id)
		if err != nil {
			return err
		}

		if cmd.Bool("json") {
			return output.JSON(w, t)
		}

		_, err = fmt.Fprintln(w, t.GetBody())

		return err
	}

	list, err := c.Previews.ListTexts(ctx, connect.NewRequest(&apiv1.ListTextsRequest{PreviewId: id, Limit: int32(cmd.Int("limit"))}))
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		return output.JSON(w, list.Msg)
	}

	texts := list.Msg.GetTexts()

	if len(texts) == 0 {
		fmt.Fprintln(w, "No text messages caught yet.")

		return nil
	}

	rows := make([][]string, 0, len(texts))

	for _, t := range texts {
		rows = append(rows, []string{t.GetSendTime().AsTime().Local().Format(time.DateTime), t.GetTo(), oneLine(t.GetBody())})
	}

	return output.Table(w, []string{"sent", "to", "text"}, rows)
}

// oneLine is a text message's body on one line, for the table.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// waitForText waits for a text message that wasn't there when it started, to a number containing --to.
func waitForText(ctx context.Context, cmd *cli.Command, c *api.Clients, id string) (*apiv1.TextMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
	defer cancel()

	list := func() ([]*apiv1.TextMessage, error) {
		res, err := c.Previews.ListTexts(ctx, connect.NewRequest(&apiv1.ListTextsRequest{PreviewId: id, Limit: 500}))
		if err != nil {
			return nil, err
		}

		return res.Msg.GetTexts(), nil
	}

	before, err := list()
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}

	for _, t := range before {
		seen[t.GetId()] = true
	}

	fmt.Fprintln(cmd.Root().ErrWriter, "Waiting for a text message…")

	for {
		select {
		case <-ctx.Done():
			return nil, cli.Exit(fmt.Sprintf("no matching text message arrived within %s", cmd.Duration("timeout")), 1)
		case <-time.After(mailPoll):
		}

		now, err := list()
		if err != nil {
			if ctx.Err() != nil {
				continue
			}

			return nil, err
		}

		// Oldest new one first: the one the app sent first.
		for i := len(now) - 1; i >= 0; i-- {
			t := now[i]

			if !seen[t.GetId()] && strings.Contains(t.GetTo(), cmd.String("to")) {
				return t, nil
			}
		}
	}
}
