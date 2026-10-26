package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	"github.com/stagdhq/stagd/internal/client/api"
	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func previewMailCommand() *cli.Command {
	return &cli.Command{
		Name:      "mail",
		Usage:     "read the mail a preview captured (stagd.yaml services: { mail: {} })",
		ArgsUsage: "<id> [n | message-id]",
		Description: "Without a message, lists what the preview captured, newest first and numbered.\n" +
			"With a number from that list (or a message id), prints that message as text.\n" +
			"Nothing a preview sends is delivered; it waits here.\n\n" +
			"--wait waits for the next message to arrive (one matching --to and --subject, when given)\n" +
			"and prints it, so a script can sign up in the preview and then read the confirmation:\n\n" +
			"   stagd preview mail <id> --wait --to kari@acme.test --subject confirm\n\n" +
			"It exits non-zero when nothing arrives before --timeout.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "html", Usage: "print the message's HTML instead of its text"},
			&cli.BoolFlag{Name: "clear", Usage: "delete everything the preview captured"},
			&cli.IntFlag{Name: "limit", Usage: "list at most this many", Value: 50},
			&cli.BoolFlag{Name: "wait", Usage: "wait for the next message to arrive, then print it"},
			&cli.StringFlag{Name: "to", Usage: "with --wait: only a message to this address (or part of it)"},
			&cli.StringFlag{Name: "subject", Usage: "with --wait: only a message whose subject contains this"},
			&cli.DurationFlag{Name: "timeout", Usage: "with --wait: give up after this long", Value: 2 * time.Minute},
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

	if cmd.Bool("wait") {
		if which != "" {
			return cli.Exit("--wait waits for the next message; leave out the message", 2)
		}

		m, err := waitForMail(ctx, cmd, c, id)
		if err != nil {
			return err
		}

		return printMail(ctx, cmd, c, id, m)
	}

	limit := cmd.Int("limit")
	list, err := c.Previews.ListMail(ctx, connect.NewRequest(&apiv1.ListMailRequest{PreviewId: id, Limit: int32(limit)}))
	if err != nil {
		return err
	}

	messages := list.Msg.GetMessages()

	if which == "" {
		if cmd.Bool("json") {
			return output.JSON(w, list.Msg)
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

	fmt.Fprintf(w, "Subject: %s\n", s.GetSubject())
	fmt.Fprintf(w, "Date:    %s (captured, not delivered)\n\n", s.GetReceiveTime().AsTime().Local().Format(time.DateTime))

	body := m.GetText()
	if cmd.Bool("html") || body == "" {
		body = m.GetHtml()
	}

	fmt.Fprintln(w, body)

	return nil
}

// mailPoll is how often --wait asks; a variable so tests needn't wait.
var mailPoll = 2 * time.Second

func matches(m *apiv1.MailSummary, to, subject string) bool {
	if subject != "" && !strings.Contains(strings.ToLower(m.GetSubject()), strings.ToLower(subject)) {
		return false
	}

	if to == "" {
		return true
	}

	for _, a := range m.GetTo() {
		if strings.Contains(strings.ToLower(a.GetAddress()), strings.ToLower(to)) {
			return true
		}
	}

	return false
}

// waitForMail returns the id of the first message that arrives after it
// starts (what's already there doesn't count) and matches --to and --subject.
func waitForMail(ctx context.Context, cmd *cli.Command, c *api.Clients, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
	defer cancel()

	list := func() ([]*apiv1.MailSummary, error) {
		res, err := c.Previews.ListMail(ctx, connect.NewRequest(&apiv1.ListMailRequest{PreviewId: id, Limit: 500}))
		if err != nil {
			return nil, err
		}

		return res.Msg.GetMessages(), nil
	}

	before, err := list()
	if err != nil {
		return "", err
	}

	seen := map[string]bool{}
	for _, m := range before {
		seen[m.GetId()] = true
	}

	fmt.Fprintln(cmd.Root().ErrWriter, "Waiting for mail…")

	for {
		select {
		case <-ctx.Done():
			return "", cli.Exit(fmt.Sprintf("no matching mail arrived within %s", cmd.Duration("timeout")), 1)
		case <-time.After(mailPoll):
		}

		now, err := list()
		if err != nil {
			if ctx.Err() != nil {
				continue
			}

			return "", err
		}

		// Oldest new message first: the one the app sent first.
		for i := len(now) - 1; i >= 0; i-- {
			m := now[i]
			if !seen[m.GetId()] && matches(m, cmd.String("to"), cmd.String("subject")) {
				return m.GetId(), nil
			}
		}
	}
}
