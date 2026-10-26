package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"charm.land/huh/v2"
	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func secretsCommand() *cli.Command {
	repoFlag := &cli.StringFlag{Name: "repo", Usage: "a repository's own secrets, by owner/name; without it, the org's"}

	return &cli.Command{
		Name:  "secrets",
		Usage: "the secrets previews get as variables",
		Description: "Previews get the org's secrets and their repository's own, which win by name.\n" +
			"A compose file uses them like any variable: ${SESSION_SECRET}, or a bare\n" +
			"`- SESSION_SECRET` under environment. Values are encrypted and never shown again.",
		Commands: []*cli.Command{
			{
				Name:      "set",
				Usage:     "create or replace secrets",
				ArgsUsage: "NAME=value | NAME=@- | NAME …",
				Description: "NAME=@- reads the value from stdin; a NAME alone asks for it without echoing.\n" +
					"Values on the command line end up in your shell's history: prefer the other two.",
				Flags:  []cli.Flag{repoFlag},
				Action: secretsSet,
			},
			{
				Name:   "list",
				Usage:  "list secrets by name",
				Flags:  []cli.Flag{repoFlag},
				Action: secretsList,
			},
			{
				Name:      "delete",
				Usage:     "delete a secret",
				ArgsUsage: "NAME",
				Flags: []cli.Flag{
					repoFlag,
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "don't ask for confirmation"},
				},
				Action: secretsDelete,
			},
		},
	}
}

// secretValues reads NAME=value, NAME=@- (stdin) and NAME (asked for).
func secretValues(ctx context.Context, args []string) (map[string]string, error) {
	values := map[string]string{}
	readStdin := false

	for _, arg := range args {
		name, value, given := strings.Cut(arg, "=")

		switch {
		case name == "":
			return nil, fmt.Errorf("%q: use NAME=value", arg)

		case given && value == "@-":
			if readStdin {
				return nil, errors.New("only one secret can be read from stdin")
			}

			readStdin = true

			raw, err := io.ReadAll(bufio.NewReader(stdin))
			if err != nil {
				return nil, err
			}

			value = strings.TrimRight(string(raw), "\r\n")

		case !given:
			if !interactive() {
				return nil, fmt.Errorf("%s has no value: use %s=@- to read it from stdin", name, name)
			}

			input := huh.NewInput().Title(name).EchoMode(huh.EchoModePassword).Value(&value)
			if err := newForm(huh.NewGroup(input)).RunWithContext(ctx); err != nil {
				return nil, err
			}
		}

		values[name] = value
	}

	return values, nil
}

func secretsSet(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() == 0 {
		return cli.Exit("usage: stagd secrets set [--repo owner/name] NAME=value | NAME=@- | NAME …", 2)
	}

	values, err := secretValues(ctx, cmd.Args().Slice())
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Secrets.SetSecrets(ctx, connect.NewRequest(&apiv1.SetSecretsRequest{Repository: cmd.String("repo"), Values: values}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	names := make([]string, 0, len(values))

	for name := range values {
		names = append(names, name)
	}

	sort.Strings(names)

	fmt.Fprintf(w, "Set %s in %s. Previews get it on their next deploy.\n", strings.Join(names, ", "), scopeWords(cmd.String("repo")))

	for _, name := range res.Msg.GetUnmasked() {
		fmt.Fprintf(w, "Note: %s is short enough that Stagd can't hide it in build and start logs.\n", name)
	}

	return nil
}

func scopeWords(repo string) string {
	if repo == "" {
		return "the org"
	}

	return repo
}

func secretsList(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Secrets.ListSecrets(ctx, connect.NewRequest(&apiv1.ListSecretsRequest{Repository: cmd.String("repo")}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	if len(res.Msg.GetSecrets()) == 0 {
		_, err := fmt.Fprintf(w, "No secrets in %s.\n", scopeWords(cmd.String("repo")))

		return err
	}

	rows := make([][]string, 0, len(res.Msg.GetSecrets()))

	for _, s := range res.Msg.GetSecrets() {
		rows = append(rows, []string{s.GetName(), s.GetUpdateTime().AsTime().Local().Format(time.DateTime), s.GetUpdatedBy()})
	}

	return output.Table(w, []string{"name", "updated", "by"}, rows)
}

func secretsDelete(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return cli.Exit("usage: stagd secrets delete [--repo owner/name] NAME", 2)
	}

	if !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("pass --yes to delete without a prompt", 2)
		}

		if ok, err := confirm(cmd, fmt.Sprintf("Delete %s from %s?", name, scopeWords(cmd.String("repo"))), "Previews keep it until their next deploy."); err != nil || !ok {
			return err
		}
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	if _, err := c.Secrets.DeleteSecret(ctx, connect.NewRequest(&apiv1.DeleteSecretRequest{Repository: cmd.String("repo"), Name: name})); err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "Deleted %s from %s.\n", name, scopeWords(cmd.String("repo")))

	return err
}

func activityCommand() *cli.Command {
	return &cli.Command{
		Name:  "activity",
		Usage: "who changed what in the org: secrets, compute, access",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "limit", Usage: "at most this many, newest first", Value: 50},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := clients(cmd)
			if err != nil {
				return err
			}

			res, err := c.Activity.ListActivity(ctx, connect.NewRequest(&apiv1.ListActivityRequest{PageSize: int32(cmd.Int("limit"))}))
			if err != nil {
				return err
			}

			w := cmd.Root().Writer

			if cmd.Bool("json") {
				return output.JSON(w, res.Msg)
			}

			rows := make([][]string, 0, len(res.Msg.GetActivity()))

			for _, a := range res.Msg.GetActivity() {
				who := a.GetActor()
				if who == "" {
					who = "Stagd"
				}

				rows = append(rows, []string{a.GetCreateTime().AsTime().Local().Format(time.DateTime), who, a.GetVia(), a.GetAction(), a.GetTarget()})
			}

			return output.Table(w, []string{"when", "who", "via", "what", "on"}, rows)
		},
	}
}
