package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/durationpb"

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
			"never sent to a phone (only those matching --to and --body, when given).\n\n" +
			"--wait waits for the next matching one and prints its text, so a script can sign in to the\n" +
			"preview with the code it's sent:\n\n" +
			"   stagd preview texts <id> --wait --to \"+47 912 34 567\"\n\n" +
			"It exits non-zero when nothing arrives before --timeout. As with stagd preview mail, --cursor\n" +
			"and --after count only what a step sent, and --none checks that it sent nothing.",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "limit", Usage: "list at most this many", Value: 50},
			&cli.StringFlag{Name: "to", Usage: "only texts to a number containing this (by its digits)"},
			&cli.StringFlag{Name: "body", Usage: "only texts containing this"},
			&cli.StringFlag{Name: "after", Usage: "only texts that arrived after this cursor (from --cursor, or an earlier --wait)"},
			&cli.BoolFlag{Name: "cursor", Usage: "print where the texts stand now, for --after, and nothing else"},
			&cli.BoolFlag{Name: "wait", Usage: "wait for the next matching text message to arrive, then print it"},
			&cli.BoolFlag{Name: "none", Usage: "wait out --timeout and fail if a matching text message arrives"},
			&cli.DurationFlag{Name: "timeout", Usage: "with --wait or --none: how long to wait", Value: 2 * time.Minute},
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

	if cmd.Bool("cursor") {
		cursor, err := textsCursor(ctx, c, id)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintln(w, cursor)

		return err
	}

	if cmd.Bool("wait") || cmd.Bool("none") {
		if cmd.Bool("wait") && cmd.Bool("none") {
			return cli.Exit("--wait waits for a text message and --none for there to be none: one of them", 2)
		}

		found, cursor, err := waitForText(ctx, cmd, c, id)
		if err != nil {
			return err
		}

		fmt.Fprintf(cmd.Root().ErrWriter, "cursor: %s\n", cursor)

		if cmd.Bool("none") {
			if len(found) > 0 {
				t := found[len(found)-1]

				return cli.Exit(fmt.Sprintf("a text message arrived: %q to %s", oneLine(t.GetBody()), t.GetTo()), 1)
			}

			fmt.Fprintf(w, "No matching text message arrived within %s.\n", cmd.Duration("timeout"))

			return nil
		}

		if len(found) == 0 {
			return cli.Exit(fmt.Sprintf("no matching text message arrived within %s", cmd.Duration("timeout")), 1)
		}

		// Oldest first: the one the app sent first.
		t := found[len(found)-1]

		if cmd.Bool("json") {
			return output.JSON(w, t)
		}

		_, err = fmt.Fprintln(w, t.GetBody())

		return err
	}

	list, err := c.Previews.ListTexts(ctx, connect.NewRequest(&apiv1.ListTextsRequest{PreviewId: id, Limit: int32(cmd.Int("limit")), After: cmd.String("after"), Filter: textFilter(cmd)}))
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		return output.JSON(w, list.Msg)
	}

	texts := list.Msg.GetTexts()

	if len(texts) == 0 && narrowed(cmd, "to", "body", "after") {
		fmt.Fprintln(w, "No matching text messages.")

		return nil
	}

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

func textFilter(cmd *cli.Command) *apiv1.TextFilter {
	return &apiv1.TextFilter{To: cmd.String("to"), Body: cmd.String("body")}
}

// textsCursor is where the preview's text messages stand now.
func textsCursor(ctx context.Context, c *api.Clients, id string) (string, error) {
	res, err := c.Previews.ListTexts(ctx, connect.NewRequest(&apiv1.ListTextsRequest{PreviewId: id, Limit: 1}))
	if err != nil {
		return "", err
	}

	return res.Msg.GetCursor(), nil
}

// waitForText waits for a text message matching --to and --body that
// arrives after --after, or after now without it: the matching ones, newest
// first, none when --timeout passed, and where the texts stood at the end.
func waitForText(ctx context.Context, cmd *cli.Command, c *api.Clients, id string) ([]*apiv1.TextMessage, string, error) {
	after := cmd.String("after")

	if after == "" {
		var err error

		if after, err = textsCursor(ctx, c, id); err != nil {
			return nil, "", err
		}
	}

	fmt.Fprintln(cmd.Root().ErrWriter, "Waiting for a text message…")

	return waitAfter(ctx, cmd.Duration("timeout"), after, func(ctx context.Context, after string, d time.Duration) ([]*apiv1.TextMessage, string, error) {
		res, err := c.Previews.WaitForTexts(ctx, connect.NewRequest(&apiv1.WaitForTextsRequest{
			PreviewId: id,
			After:     after,
			Filter:    textFilter(cmd),
			Timeout:   durationpb.New(d),
		}))
		if err != nil {
			return nil, "", err
		}

		return res.Msg.GetTexts(), res.Msg.GetCursor(), nil
	})
}
