package cli

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

// authCommand is where you're signed in: browsers and CLIs, and ending them.
// Signing in and out are `stagd login` and `stagd logout`.
func authCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "where you're signed in to Stagd, and signing out of those places",
		Commands: []*cli.Command{
			{
				Name:   "sessions",
				Usage:  "list where you're signed in: browsers and CLIs, most recently used first",
				Action: authSessions,
			},
			{
				Name:      "revoke",
				Usage:     "sign one session out (an id from `stagd auth sessions`), or all but this one with --others",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "others", Usage: "every session but this one"},
				},
				Action: authRevoke,
			},
		},
	}
}

func authSessions(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Account.ListSessions(ctx, connect.NewRequest(&apiv1.ListSessionsRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	rows := make([][]string, 0, len(res.Msg.GetSessions()))

	for _, s := range res.Msg.GetSessions() {
		id := s.GetId()

		if s.GetCurrent() {
			id += " (this one)"
		}

		rows = append(rows, []string{
			sessionKind(s.GetKind()),
			sessionWhere(s),
			s.GetSignInMethod(),
			s.GetLastSeenTime().AsTime().Local().Format("2006-01-02 15:04"),
			s.GetExpireTime().AsTime().Local().Format("2006-01-02"),
			id,
		})
	}

	return output.Table(w, []string{"kind", "where", "signed in with", "last used", "expires", "id"}, rows)
}

func authRevoke(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().First()
	others := cmd.Bool("others")

	if (id == "") == !others {
		return cli.Exit("usage: stagd auth revoke <id>, or stagd auth revoke --others", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if others {
		res, err := c.Account.RevokeOtherSessions(ctx, connect.NewRequest(&apiv1.RevokeOtherSessionsRequest{}))
		if err != nil {
			return err
		}

		fmt.Fprintf(w, "Signed out of %s; this one stays signed in.\n", plural(int(res.Msg.GetRevoked()), "other session", "other sessions"))

		return nil
	}

	// Revoking this one is signing out: its stored token stops working, so it goes too.
	list, err := c.Account.ListSessions(ctx, connect.NewRequest(&apiv1.ListSessionsRequest{}))
	if err != nil {
		return err
	}

	current := false

	for _, s := range list.Msg.GetSessions() {
		if s.GetId() == id && s.GetCurrent() {
			current = true
		}
	}

	if _, err := c.Account.RevokeSession(ctx, connect.NewRequest(&apiv1.RevokeSessionRequest{Id: id})); err != nil {
		return err
	}

	if current {
		if cmd.String("token") == "" {
			if err := store.Delete(cmd.String("api-url")); err != nil {
				return err
			}
		}

		fmt.Fprintln(w, "Signed out: that was this session.")

		return nil
	}

	fmt.Fprintf(w, "Signed out session %s.\n", id)

	return nil
}

func sessionKind(k apiv1.SessionKind) string {
	switch k {
	case apiv1.SessionKind_SESSION_KIND_CLI:
		return "cli"
	case apiv1.SessionKind_SESSION_KIND_WEB:
		return "browser"
	default:
		return "?"
	}
}

// sessionWhere is what a session says about where it is: a CLI's machine and
// version, a browser and its system ("Chrome on macOS").
func sessionWhere(s *apiv1.Session) string {
	if s.GetKind() == apiv1.SessionKind_SESSION_KIND_CLI {
		parts := []string{}

		for _, p := range []string{s.GetDeviceName(), s.GetClientPlatform(), s.GetClientVersion()} {
			if p != "" {
				parts = append(parts, p)
			}
		}

		if len(parts) > 0 {
			return strings.Join(parts, " · ")
		}
	}

	return browserOf(s.GetUserAgent())
}

// browserOf reads a user agent the rough way: the first browser and system
// it names, in the order that tells them apart (Edge says Chrome too, Chrome
// says Safari, Android says Linux).
func browserOf(ua string) string {
	if ua == "" {
		return ""
	}

	pick := func(names [][2]string) string {
		for _, n := range names {
			if strings.Contains(ua, n[0]) {
				return n[1]
			}
		}

		return ""
	}

	browser := pick([][2]string{{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"}})
	system := pick([][2]string{{"iPhone", "iOS"}, {"iPad", "iPadOS"}, {"Android", "Android"}, {"Mac OS X", "macOS"}, {"Windows", "Windows"}, {"Linux", "Linux"}})

	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case len(ua) > 40:
		return ua[:39] + "…"
	default:
		return ua
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}

	return fmt.Sprintf("%d %s", n, many)
}
