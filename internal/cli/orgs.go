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

	"github.com/stagdhq/stagd/internal/client/api"
	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
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
				Usage:  "list the people in the current org; its owner is marked",
				Action: orgsMembers,
				Commands: []*cli.Command{
					{
						Name:      "transfer",
						Usage:     "hand the current org to another of its admins (the owner only); you stay an admin",
						ArgsUsage: "<email|login>",
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "don't ask for confirmation"},
						},
						Action: orgsTransfer,
					},
				},
			},
			{
				Name:      "delete",
				Usage:     "delete the current org and everything Stagd keeps for it (its owner)",
				ArgsUsage: "<slug>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Usage: "don't ask: the slug given is the confirmation"},
					&cli.StringFlag{Name: "reason", Usage: "why: " + strings.Join(leaveReasonNames, ", ")},
					&cli.StringFlag{Name: "why", Usage: "why, in your own words"},
				},
				Action: orgsDelete,
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

// interactive is whether someone is at the keyboard; replaced in tests.
var interactive = func() bool {
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

		form := newForm(huh.NewGroup(
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
		role := m.GetRole()
		if m.GetOwner() {
			role += " (owner)"
		}

		rows = append(rows, []string{m.GetUser().GetName(), m.GetUser().GetEmail(), role})
	}

	return output.Table(w, []string{"name", "email", "role"}, rows)
}

// orgsTransfer hands the current org to another admin, found by email or login, confirmed at the keyboard unless --yes.
func orgsTransfer(ctx context.Context, cmd *cli.Command) error {
	who := strings.TrimSpace(cmd.Args().First())
	if who == "" || cmd.Args().Len() > 1 {
		return cli.Exit("usage: stagd orgs members transfer [--yes] <email|login>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Orgs.ListMembers(ctx, connect.NewRequest(&apiv1.ListMembersRequest{}))
	if err != nil {
		return err
	}

	var to *apiv1.Member

	for _, m := range res.Msg.GetMembers() {
		if strings.EqualFold(m.GetUser().GetEmail(), who) || strings.EqualFold(m.GetLogin(), who) || m.GetUser().GetId() == who {
			to = m
		}
	}

	if to == nil {
		return cli.Exit(fmt.Sprintf("nobody called %s in this org (see stagd orgs members)", who), 1)
	}

	if to.GetRoleKind() != apiv1.Role_ROLE_ADMIN {
		return cli.Exit(fmt.Sprintf("%s is a %s: only an admin can own the org, so make them one first", who, to.GetRole()), 1)
	}

	if !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("pass --yes to hand the org on without a prompt", 2)
		}

		if ok, err := confirm(cmd, fmt.Sprintf("Make %s the owner?", who), "They'll be the one who can delete the org or hand it on; you stay an admin."); err != nil || !ok {
			return err
		}
	}

	if _, err := c.Orgs.TransferOwnership(ctx, connect.NewRequest(&apiv1.TransferOwnershipRequest{UserId: to.GetUser().GetId()})); err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "%s owns the org now; you're an admin.\n", who)

	return err
}

// leaveReasons are DeleteOrg's reasons, as --reason takes them.
var leaveReasons = map[string]apiv1.LeaveReason{
	"too-expensive":   apiv1.LeaveReason_LEAVE_REASON_TOO_EXPENSIVE,
	"missing-feature": apiv1.LeaveReason_LEAVE_REASON_MISSING_FEATURE,
	"hard-to-set-up":  apiv1.LeaveReason_LEAVE_REASON_HARD_TO_SET_UP,
	"other-tool":      apiv1.LeaveReason_LEAVE_REASON_OTHER_TOOL,
	"just-trying":     apiv1.LeaveReason_LEAVE_REASON_JUST_TRYING,
	"other":           apiv1.LeaveReason_LEAVE_REASON_OTHER,
}

var leaveReasonNames = []string{"too-expensive", "missing-feature", "hard-to-set-up", "other-tool", "just-trying", "other"}

// orgsDelete deletes the current org: its slug typed as confirmation, asked again at the keyboard
// unless --yes, and why, if they'll say.
func orgsDelete(ctx context.Context, cmd *cli.Command) error {
	slug := strings.TrimSpace(cmd.Args().First())
	if slug == "" {
		return cli.Exit("usage: stagd orgs delete <slug> (the current org's)", 2)
	}

	reasonName := strings.TrimSpace(cmd.String("reason"))
	why := strings.TrimSpace(cmd.String("why"))

	reason, ok := leaveReasons[reasonName]
	if reasonName != "" && !ok {
		return cli.Exit("--reason is one of "+strings.Join(leaveReasonNames, ", "), 2)
	}

	if !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("pass --yes to delete without a prompt", 2)
		}

		typed := ""
		options := []huh.Option[string]{huh.NewOption("Skip", "")}

		for _, name := range leaveReasonNames {
			options = append(options, huh.NewOption(name, name))
		}

		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("Delete "+slug+"?").
				Description("Its previews stop now, and everyone in it loses access. Its connections, repositories,\nsecrets, seeds and activity are deleted in 30 days, and Stagd's GitHub App uninstalled;\nuntil then Stagd's support can restore it. Type the slug again to confirm.").
				Value(&typed).
				Validate(func(s string) error {
					if strings.TrimSpace(s) != slug {
						return fmt.Errorf("type %s", slug)
					}

					return nil
				}),
			huh.NewSelect[string]().Title("Why are you leaving? (optional)").Options(options...).Value(&reasonName),
			huh.NewInput().Title("Anything else? (optional)").Value(&why),
		))

		if err := form.RunWithContext(ctx); err != nil {
			return err
		}

		reason = leaveReasons[reasonName]
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Orgs.DeleteOrg(ctx, connect.NewRequest(&apiv1.DeleteOrgRequest{Slug: slug, Reason: reason, ReasonText: strings.TrimSpace(why)}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	fmt.Fprintf(w, "Deleted %s. Its previews are being torn down, and its members get an email.\nEverything else goes in 30 days; until then Stagd's support can restore it.\n", slug)

	if compute := res.Msg.GetCompute(); len(compute) > 0 {
		fmt.Fprintf(w, "Stagd's access is still installed on your side for: %s.\nRemove it with stagd compute uninstall, or delete the manifests you applied.\n", strings.Join(compute, ", "))
	}

	return nil
}
