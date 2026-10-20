package cli

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"charm.land/huh/v2"
	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"stagd.dev/stagd/internal/client/api"
	apiv1 "stagd.dev/stagd/internal/gen/stagd/api/v1"
	"stagd.dev/stagd/internal/output"
)

// getMe asks the API who a token belongs to.
func getMe(ctx context.Context, cmd *cli.Command, token string) (*apiv1.GetMeResponse, error) {
	c, err := api.New(api.Config{
		BaseURL:   cmd.String("api-url"),
		Token:     token,
		UserAgent: "stagd-cli/" + cmd.Root().Version,
	})
	if err != nil {
		return nil, err
	}

	res, err := c.Auth.GetMe(ctx, connect.NewRequest(&apiv1.GetMeRequest{}))
	if err != nil {
		return nil, err
	}

	return res.Msg, nil
}

// membershipLabel is "Acme · acme (owner)".
func membershipLabel(m *apiv1.Membership) string {
	if m.GetOrg().GetSlug() == "" {
		return "no org yet"
	}

	return fmt.Sprintf("%s · %s (%s)", m.GetOrg().GetName(), m.GetOrg().GetSlug(), m.GetRole())
}

func whoamiCommand() *cli.Command {
	return &cli.Command{
		Name:  "whoami",
		Usage: "show who you're signed in as, and your org",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			t := token(cmd)

			if t == "" {
				return errNotSignedIn
			}

			me, err := getMe(ctx, cmd, t)
			if err != nil {
				return err
			}

			w := cmd.Root().Writer

			if cmd.Bool("json") {
				return output.JSON(w, me)
			}

			fmt.Fprintf(w, "%s (%s)\n", me.GetUser().GetEmail(), me.GetUser().GetName())
			fmt.Fprintf(w, "org: %s\n", membershipLabel(me.GetActive()))

			if n := len(me.GetMemberships()); n > 1 {
				fmt.Fprintf(w, "member of %d orgs (stagd orgs list)\n", n)
			}

			return nil
		},
	}
}

func orgsCommand() *cli.Command {
	return &cli.Command{
		Name:  "orgs",
		Usage: "create, see and switch between your orgs",
		Commands: []*cli.Command{
			{
				Name:   "list",
				Usage:  "list the orgs you belong to",
				Action: orgsList,
			},
			{
				Name:      "create",
				Usage:     "create an org and switch to it",
				ArgsUsage: "[slug]",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Usage: "display name, e.g. \"Acme Inc\""},
				},
				Action: orgsCreate,
			},
			{
				Name:      "switch",
				Usage:     "act in another of your orgs",
				ArgsUsage: "<slug>",
				Action:    orgsSwitch,
			},
			{
				Name:   "members",
				Usage:  "list the people in the current org",
				Action: orgsMembers,
			},
		},
	}
}

func orgsList(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Orgs.ListOrgs(ctx, connect.NewRequest(&apiv1.ListOrgsRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	rows := make([][]string, 0, len(res.Msg.GetMemberships()))

	for _, m := range res.Msg.GetMemberships() {
		marker := ""
		if m.GetOrg().GetId() == res.Msg.GetActiveOrgId() {
			marker = "*"
		}

		rows = append(rows, []string{marker, m.GetOrg().GetSlug(), m.GetOrg().GetName(), m.GetRole()})
	}

	return output.Table(w, []string{"", "slug", "name", "role"}, rows)
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func validSlug(s string) error {
	if len(s) < 2 || len(s) > 39 || !slugPattern.MatchString(s) {
		return fmt.Errorf("use 2–39 lower-case letters, digits and dashes, e.g. acme")
	}

	return nil
}

// slugFrom turns "Acme Inc" into "acme-inc".
func slugFrom(name string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}

	return strings.Trim(b.String(), "-")
}

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func orgsCreate(ctx context.Context, cmd *cli.Command) error {
	name := strings.TrimSpace(cmd.String("name"))
	slug := strings.TrimSpace(cmd.Args().First())

	// Ask for what's missing when someone is at the keyboard; scripts must pass both.
	if name == "" || slug == "" {
		if !interactive() {
			return cli.Exit("usage: stagd orgs create <slug> --name <name>", 2)
		}

		if slug == "" && name != "" {
			slug = slugFrom(name)
		}

		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Org name").Placeholder("Acme Inc").Value(&name).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("give the org a name")
					}

					return nil
				}),
			huh.NewInput().Title("Slug").Description("Used in URLs and the CLI").
				PlaceholderFunc(func() string { return slugFrom(name) }, &name).
				Value(&slug).
				Validate(func(s string) error {
					if s == "" {
						return nil
					}

					return validSlug(s)
				}),
		))

		if err := form.RunWithContext(ctx); err != nil {
			return err
		}

		if slug == "" {
			slug = slugFrom(name)
		}
	}

	if err := validSlug(slug); err != nil {
		return cli.Exit("slug: "+err.Error(), 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Orgs.CreateOrg(ctx, connect.NewRequest(&apiv1.CreateOrgRequest{Name: strings.TrimSpace(name), Slug: slug}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	_, err = fmt.Fprintf(w, "Created %s; you're in it now\n", membershipLabel(res.Msg.GetMembership()))

	return err
}

func orgsSwitch(ctx context.Context, cmd *cli.Command) error {
	slug := strings.TrimSpace(cmd.Args().First())
	if slug == "" {
		return cli.Exit("usage: stagd orgs switch <slug>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Orgs.SwitchOrg(ctx, connect.NewRequest(&apiv1.SwitchOrgRequest{Slug: slug}))
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "Now in %s\n", membershipLabel(res.Msg.GetActive()))

	return err
}

func orgsMembers(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Orgs.ListMembers(ctx, connect.NewRequest(&apiv1.ListMembersRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	rows := make([][]string, 0, len(res.Msg.GetMembers()))

	for _, m := range res.Msg.GetMembers() {
		rows = append(rows, []string{m.GetUser().GetName(), m.GetUser().GetEmail(), m.GetRole()})
	}

	return output.Table(w, []string{"name", "email", "role"}, rows)
}
