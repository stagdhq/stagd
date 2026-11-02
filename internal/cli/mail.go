package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/stagdhq/stagd/internal/client/api"
	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func previewMailCommand() *cli.Command {
	return &cli.Command{
		Name:      "mail",
		Usage:     "read the mail a preview captured (stagd.yaml services: { mail: {} })",
		ArgsUsage: "<id> [n | message-id]",
		Description: "Without a message, lists what the preview captured, newest first and numbered\n" +
			"(only what matches --to, --from and --subject, when given). With a number from that list\n" +
			"(or a message id), prints that message as text, with its links, its attachments and any\n" +
			"template text it left unfilled ({{name}}, undefined). Nothing a preview sends is delivered;\n" +
			"it waits here.\n\n" +
			"--wait waits for the next matching message to arrive and prints it, so a script can sign up\n" +
			"in the preview and then read the confirmation:\n\n" +
			"   stagd preview mail <id> --wait --to kari@acme.test --subject confirm\n\n" +
			"It exits non-zero when nothing arrives before --timeout. To count only what a step sent,\n" +
			"take the cursor first and wait after it; --none checks that a step sent nothing:\n\n" +
			"   after=$(stagd preview mail <id> --cursor)\n" +
			"   ...the step...\n" +
			"   stagd preview mail <id> --wait --after \"$after\" --to bob@globex.test\n" +
			"   stagd preview mail <id> --none --after \"$after\" --to kari@ --timeout 10s",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "html", Usage: "print the message's HTML instead of its text"},
			&cli.BoolFlag{Name: "clear", Usage: "delete everything the preview captured"},
			&cli.IntFlag{Name: "limit", Usage: "list at most this many", Value: 50},
			&cli.StringFlag{Name: "to", Usage: "only mail to this address or name, or part of it (To, Cc or Bcc)"},
			&cli.StringFlag{Name: "from", Usage: "only mail from this address or name, or part of it"},
			&cli.StringFlag{Name: "subject", Usage: "only mail whose subject contains this"},
			&cli.StringFlag{Name: "after", Usage: "only mail that arrived after this cursor (from --cursor, or an earlier --wait)"},
			&cli.BoolFlag{Name: "cursor", Usage: "print where the inbox stands now, for --after, and nothing else"},
			&cli.BoolFlag{Name: "wait", Usage: "wait for the next matching message to arrive, then print it"},
			&cli.BoolFlag{Name: "none", Usage: "wait out --timeout and fail if a matching message arrives"},
			&cli.DurationFlag{Name: "timeout", Usage: "with --wait or --none: how long to wait", Value: 2 * time.Minute},
		},
		Action: previewMail,
	}
}

func address(a *apiv1.MailAddress) string {
	if a == nil {
		return ""
	}

	if a.GetName() != "" {
		return fmt.Sprintf("%s <%s>", a.GetName(), a.GetAddress())
	}

	return a.GetAddress()
}

func addresses(as []*apiv1.MailAddress) string {
	s := ""

	for i, a := range as {
		if i > 0 {
			s += ", "
		}

		s += address(a)
	}

	return s
}

func previewMail(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().Get(0)
	which := cmd.Args().Get(1)

	if id == "" {
		return cli.Exit("usage: stagd preview mail <id> [n | message-id]", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("clear") {
		if _, err := c.Previews.ClearMail(ctx, connect.NewRequest(&apiv1.ClearMailRequest{PreviewId: id})); err != nil {
			return err
		}

		fmt.Fprintln(w, "Cleared the preview's captured mail.")

		return nil
	}

	if cmd.Bool("cursor") {
		cursor, err := mailCursor(ctx, c, id)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintln(w, cursor)

		return err
	}

	if cmd.Bool("wait") || cmd.Bool("none") {
		if which != "" {
			return cli.Exit("--wait and --none wait for what arrives; leave out the message", 2)
		}

		if cmd.Bool("wait") && cmd.Bool("none") {
			return cli.Exit("--wait waits for a message and --none for there to be none: one of them", 2)
		}

		found, cursor, err := waitForMail(ctx, cmd, c, id)
		if err != nil {
			return err
		}

		fmt.Fprintf(cmd.Root().ErrWriter, "cursor: %s\n", cursor)

		if cmd.Bool("none") {
			if len(found) > 0 {
				m := found[len(found)-1]

				return cli.Exit(fmt.Sprintf("mail arrived: %q to %s", m.GetSubject(), addresses(recipients(m))), 1)
			}

			fmt.Fprintf(w, "No matching mail arrived within %s.\n", cmd.Duration("timeout"))

			return nil
		}

		if len(found) == 0 {
			return cli.Exit(fmt.Sprintf("no matching mail arrived within %s", cmd.Duration("timeout")), 1)
		}

		// Oldest first: the one the app sent first.
		return printMail(ctx, cmd, c, id, found[len(found)-1].GetId())
	}

	limit := cmd.Int("limit")
	list, err := c.Previews.ListMail(ctx, connect.NewRequest(&apiv1.ListMailRequest{PreviewId: id, Limit: int32(limit), After: cmd.String("after"), Filter: mailFilter(cmd)}))
	if err != nil {
		return err
	}

	messages := list.Msg.GetMessages()

	if which == "" {
		if cmd.Bool("json") {
			return output.JSON(w, list.Msg)
		}

		if len(messages) == 0 && narrowed(cmd, "to", "from", "subject", "after") {
			fmt.Fprintln(w, "No matching mail.")

			return nil
		}

		if len(messages) == 0 {
			fmt.Fprintln(w, "No mail captured yet.")

			return nil
		}

		rows := make([][]string, 0, len(messages))

		for n, m := range messages {
			rows = append(rows, []string{
				strconv.Itoa(n + 1),
				m.GetReceiveTime().AsTime().Local().Format(time.DateTime),
				address(m.GetFrom()),
				addresses(m.GetTo()),
				m.GetSubject(),
			})
		}

		if err := output.Table(w, []string{"#", "received", "from", "to", "subject"}, rows); err != nil {
			return err
		}

		if total := int(list.Msg.GetTotal()); total > len(messages) {
			fmt.Fprintf(w, "\n%d of %d shown; --limit for more.\n", len(messages), total)
		}

		return nil
	}

	// A number from the list, or a message id.
	messageID := which
	if n, err := strconv.Atoi(which); err == nil {
		if n < 1 || n > len(messages) {
			return cli.Exit(fmt.Sprintf("there's no message %d; stagd preview mail %s lists them", n, id), 1)
		}

		messageID = messages[n-1].GetId()
	}

	return printMail(ctx, cmd, c, id, messageID)
}

func printMail(ctx context.Context, cmd *cli.Command, c *api.Clients, id, messageID string) error {
	w := cmd.Root().Writer

	res, err := c.Previews.GetMail(ctx, connect.NewRequest(&apiv1.GetMailRequest{PreviewId: id, Id: messageID}))
	if err != nil {
		return err
	}

	m := res.Msg.GetMessage()

	if cmd.Bool("json") {
		return output.JSON(w, m)
	}

	s := m.GetSummary()
	fmt.Fprintf(w, "From:    %s\n", address(s.GetFrom()))
	fmt.Fprintf(w, "To:      %s\n", addresses(s.GetTo()))

	if len(m.GetCc()) > 0 {
		fmt.Fprintf(w, "Cc:      %s\n", addresses(m.GetCc()))
	}

	if len(s.GetBcc()) > 0 {
		fmt.Fprintf(w, "Bcc:     %s\n", addresses(s.GetBcc()))
	}

	fmt.Fprintf(w, "Subject: %s\n", s.GetSubject())
	fmt.Fprintf(w, "Date:    %s (captured, not delivered)\n", s.GetReceiveTime().AsTime().Local().Format(time.DateTime))

	for _, a := range m.GetAttachments() {
		fmt.Fprintf(w, "Attached: %s (%s, %d bytes)\n", a.GetFileName(), a.GetContentType(), a.GetSizeBytes())
	}

	for _, u := range m.GetUnfilled() {
		fmt.Fprintf(w, "Unfilled: %s in the %s: %q\n", u.GetFound(), u.GetPart(), u.GetNear())
	}

	fmt.Fprintln(w)

	body := m.GetText()
	if cmd.Bool("html") || body == "" {
		body = m.GetHtml()
	}

	fmt.Fprintln(w, body)

	if links := m.GetLinks(); len(links) > 0 {
		fmt.Fprintln(w, "\nLinks:")

		for n, l := range links {
			fmt.Fprintf(w, "  %d. %s\n", n+1, l)
		}
	}

	return nil
}

// narrowed is whether any of the flags asks for only some of what was captured.
func narrowed(cmd *cli.Command, flags ...string) bool {
	for _, f := range flags {
		if cmd.String(f) != "" {
			return true
		}
	}

	return false
}

// recipients is everyone a message went to: To, Cc and Bcc.
func recipients(m *apiv1.MailSummary) []*apiv1.MailAddress {
	return append(append(append([]*apiv1.MailAddress{}, m.GetTo()...), m.GetCc()...), m.GetBcc()...)
}

func mailFilter(cmd *cli.Command) *apiv1.MailFilter {
	return &apiv1.MailFilter{To: cmd.String("to"), From: cmd.String("from"), Subject: cmd.String("subject")}
}

// mailCursor is where the preview's inbox stands now.
func mailCursor(ctx context.Context, c *api.Clients, id string) (string, error) {
	res, err := c.Previews.ListMail(ctx, connect.NewRequest(&apiv1.ListMailRequest{PreviewId: id, Limit: 1}))
	if err != nil {
		return "", err
	}

	return res.Msg.GetCursor(), nil
}

// waitForMail waits for mail matching --to, --from and --subject that
// arrives after --after, or after now without it (what's already there
// doesn't count): the matching messages, newest first, none when --timeout
// passed, and where the inbox stood at the end.
func waitForMail(ctx context.Context, cmd *cli.Command, c *api.Clients, id string) ([]*apiv1.MailSummary, string, error) {
	after := cmd.String("after")

	if after == "" {
		var err error

		if after, err = mailCursor(ctx, c, id); err != nil {
			return nil, "", err
		}
	}

	fmt.Fprintln(cmd.Root().ErrWriter, "Waiting for mail…")

	return waitAfter(ctx, cmd.Duration("timeout"), after, func(ctx context.Context, after string, d time.Duration) ([]*apiv1.MailSummary, string, error) {
		res, err := c.Previews.WaitForMail(ctx, connect.NewRequest(&apiv1.WaitForMailRequest{
			PreviewId: id,
			After:     after,
			Filter:    mailFilter(cmd),
			Timeout:   durationpb.New(d),
		}))
		if err != nil {
			return nil, "", err
		}

		return res.Msg.GetMessages(), res.Msg.GetCursor(), nil
	})
}
