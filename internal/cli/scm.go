package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

// connectPollInterval is how often `stagd github connect` asks whether the
// install finished; shortened in tests.
var connectPollInterval = 2 * time.Second

func githubCommand() *cli.Command {
	return &cli.Command{
		Name:  "github",
		Usage: "connect GitHub to your org",
		Commands: []*cli.Command{
			{
				Name:  "connect",
				Usage: "install the Stagd GitHub App on a GitHub account and link it to your org",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "no-browser", Usage: "print the link instead of opening it"},
				},
				Action: githubConnect,
			},
		},
	}
}

func githubConnect(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	start, err := c.Scm.StartConnect(ctx, connect.NewRequest(&apiv1.StartConnectRequest{
		Provider: apiv1.ScmProvider_SCM_PROVIDER_GITHUB,
	}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer
	fmt.Fprintf(w, "Install the Stagd GitHub App and pick repositories at\n  %s\n", start.Msg.GetUrl())

	if !cmd.Bool("no-browser") {
		if err := openBrowser(start.Msg.GetUrl()); err != nil {
			fmt.Fprintln(w, "(couldn't open a browser; open the link yourself)")
		}
	}

	fmt.Fprintln(w, "Waiting for GitHub…")

	deadline := start.Msg.GetExpireTime().AsTime()

	for {
		res, err := c.Scm.GetConnectStatus(ctx, connect.NewRequest(&apiv1.GetConnectStatusRequest{State: start.Msg.GetState()}))
		if err != nil {
			return err
		}

		switch res.Msg.GetStatus() {
		case apiv1.ConnectStatus_CONNECT_STATUS_CONNECTED:
			conn := res.Msg.GetConnection()

			if cmd.Bool("json") {
				return output.JSON(w, conn)
			}

			fmt.Fprintf(w, "Connected GitHub account %s\n", conn.GetAccount())
			fmt.Fprintln(w, "See its repositories with `stagd repos list`")

			return nil
		case apiv1.ConnectStatus_CONNECT_STATUS_FAILED:
			return errors.New(res.Msg.GetError())
		case apiv1.ConnectStatus_CONNECT_STATUS_EXPIRED:
			return errors.New("the link expired before GitHub sent you back; run `stagd github connect` again")
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			return errors.New("the link expired before GitHub sent you back; run `stagd github connect` again")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(connectPollInterval):
		}
	}
}

func providerLabel(p apiv1.ScmProvider) string {
	return label(p.String(), "SCM_PROVIDER_")
}

// label turns an enum name like "CONNECTION_STATUS_ACTIVE" into "active".
func label(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

func connectionsCommand() *cli.Command {
	return &cli.Command{
		Name:  "connections",
		Usage: "code host accounts connected to your org",
		Commands: []*cli.Command{
			{
				Name:   "list",
				Usage:  "list connected code host accounts",
				Action: connectionsList,
			},
			{
				Name:      "delete",
				Usage:     "disconnect a code host account from your org",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "don't ask for confirmation"},
				},
				Action: connectionsDelete,
			},
		},
	}
}

func connectionsList(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Scm.ListConnections(ctx, connect.NewRequest(&apiv1.ListConnectionsRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	rows := make([][]string, 0, len(res.Msg.GetConnections()))

	for _, conn := range res.Msg.GetConnections() {
		rows = append(rows, []string{
			conn.GetAccount(),
			providerLabel(conn.GetProvider()),
			label(conn.GetStatus().String(), "CONNECTION_STATUS_"),
			conn.GetCreatedBy().GetEmail(),
			conn.GetId(),
		})
	}

	return output.Table(w, []string{"account", "host", "status", "connected by", "id"}, rows)
}

func connectionsDelete(ctx context.Context, cmd *cli.Command) error {
	id := strings.TrimSpace(cmd.Args().First())
	if id == "" {
		return cli.Exit("usage: stagd connections delete <id>", 2)
	}

	if !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("pass --yes to disconnect without a prompt", 2)
		}

		if ok, err := confirm(cmd, "Disconnect this account?", "Its repositories stop getting previews."); err != nil || !ok {
			return err
		}
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	if _, err := c.Scm.DeleteConnection(ctx, connect.NewRequest(&apiv1.DeleteConnectionRequest{Id: id})); err != nil {
		return err
	}

	_, err = fmt.Fprintln(cmd.Root().Writer, "Disconnected.")

	return err
}

func reposCommand() *cli.Command {
	return &cli.Command{
		Name:  "repos",
		Usage: "repositories your org can preview",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "list repositories from every connected account",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "connection", Usage: "only this connection's repositories"},
					&cli.BoolFlag{Name: "all", Usage: "hidden repositories too"},
				},
				Action: reposList,
			},
			{
				Name:      "enable",
				Usage:     "turn previews on for a repository (Stagd then reads its compose file)",
				ArgsUsage: "<owner/name>",
				Action:    func(ctx context.Context, cmd *cli.Command) error { return setPreviews(ctx, cmd, true) },
			},
			{
				Name:      "disable",
				Usage:     "turn previews off for a repository and tear down its running previews",
				ArgsUsage: "<owner/name>",
				Action:    func(ctx context.Context, cmd *cli.Command) error { return setPreviews(ctx, cmd, false) },
			},
			{
				Name:      "hide",
				Usage:     "hide a repository from the org's lists (its previews carry on)",
				ArgsUsage: "<owner/name>",
				Action:    func(ctx context.Context, cmd *cli.Command) error { return setHidden(ctx, cmd, true) },
			},
			{
				Name:      "unhide",
				Usage:     "show a hidden repository in the org's lists again",
				ArgsUsage: "<owner/name>",
				Action:    func(ctx context.Context, cmd *cli.Command) error { return setHidden(ctx, cmd, false) },
			},
		},
	}
}

func setHidden(ctx context.Context, cmd *cli.Command, hidden bool) error {
	repo := strings.TrimSpace(cmd.Args().First())
	if repo == "" {
		return cli.Exit("name the repository: owner/name (see stagd repos list --all)", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Scm.SetRepositoryHidden(ctx, connect.NewRequest(&apiv1.SetRepositoryHiddenRequest{Repository: repo, Hidden: hidden}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	name := res.Msg.GetRepository().GetFullName()

	if hidden {
		_, err = fmt.Fprintf(w, "%s is hidden from the org's lists; its previews carry on. See it with stagd repos list --all.\n", name)

		return err
	}

	_, err = fmt.Fprintf(w, "%s is in the org's lists again.\n", name)

	return err
}

func setPreviews(ctx context.Context, cmd *cli.Command, enabled bool) error {
	repo := strings.TrimSpace(cmd.Args().First())
	if repo == "" {
		return cli.Exit("name the repository: owner/name (see stagd repos list)", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Scm.SetPreviewsEnabled(ctx, connect.NewRequest(&apiv1.SetPreviewsEnabledRequest{Repository: repo, Enabled: enabled}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	name := res.Msg.GetRepository().GetFullName()

	if enabled {
		_, err = fmt.Fprintf(w, "Previews are on for %s. Pull requests get one; Stagd reads compose.yaml at each commit.\n", name)

		return err
	}

	_, err = fmt.Fprintf(w, "Previews are off for %s.", name)
	if n := res.Msg.GetDestroyed(); n > 0 {
		fmt.Fprintf(w, " Tearing down %d running preview(s).", n)
	}

	fmt.Fprintln(w)

	return err
}

func reposList(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Scm.ListRepositories(ctx, connect.NewRequest(&apiv1.ListRepositoriesRequest{
		ConnectionId: cmd.String("connection"),
	}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	all := cmd.Bool("all")
	rows := make([][]string, 0, len(res.Msg.GetRepositories()))
	hiddenLeftOut := 0

	for _, r := range res.Msg.GetRepositories() {
		if r.GetHidden() && !all {
			hiddenLeftOut++

			continue
		}

		visibility := "public"
		if r.GetPrivate() {
			visibility = "private"
		}

		previews := "off"
		if r.GetPreviewsEnabled() {
			previews = "on"
		}

		if r.GetHidden() {
			previews += " (hidden)"
		}

		rows = append(rows, []string{r.GetFullName(), previews, providerLabel(r.GetProvider()), r.GetDefaultBranch(), visibility})
	}

	if err := output.Table(w, []string{"repository", "previews", "host", "default branch", "visibility"}, rows); err != nil {
		return err
	}

	if hiddenLeftOut > 0 {
		_, err = fmt.Fprintf(w, "\n%d hidden; see them with stagd repos list --all.\n", hiddenLeftOut)
	}

	return err
}
