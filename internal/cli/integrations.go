package cli

import (
	"context"
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "stagd.dev/stagd/internal/gen/stagd/api/v1"
	"stagd.dev/stagd/internal/output"
)

func integrationsCommand() *cli.Command {
	valueFlag := &cli.StringSliceFlag{Name: "value", Usage: "a field value, key=value or key=@- (stdin)"}

	return &cli.Command{
		Name:  "integrations",
		Usage: "services previews use, beside compute and GitHub: Neon for a database per preview",
		Commands: []*cli.Command{
			{
				Name:   "kinds",
				Usage:  "list the kinds of integration this server offers, and their fields",
				Action: integrationKinds,
			},
			{
				Name:      "connect",
				Usage:     "connect an integration; Stagd checks it works before saving it",
				ArgsUsage: "<kind>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Usage: "a name for it (default: the kind)"},
					valueFlag,
				},
				Action: integrationConnect,
			},
			{
				Name:   "list",
				Usage:  "list the org's integrations",
				Action: integrationList,
			},
			{
				Name:      "update",
				Usage:     "change settings, or a secret given again",
				ArgsUsage: "<name>",
				Flags:     []cli.Flag{valueFlag},
				Action:    integrationUpdate,
			},
			{
				Name:      "test",
				Usage:     "check an integration again",
				ArgsUsage: "<name>",
				Action:    integrationTest,
			},
			{
				Name:      "delete",
				Usage:     "forget an integration and its credentials",
				ArgsUsage: "<name>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "don't ask for confirmation"},
				},
				Action: integrationDelete,
			},
		},
	}
}

func integrationKinds(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Integrations.ListIntegrationKinds(ctx, connect.NewRequest(&apiv1.ListIntegrationKindsRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	for _, k := range res.Msg.GetKinds() {
		fmt.Fprintf(w, "%s (%s): %s\n", k.GetName(), k.GetId(), k.GetDescription())

		for _, f := range k.GetFields() {
			fmt.Fprintf(w, "  --value %s=…  %s. %s\n", f.GetKey(), f.GetLabel(), f.GetHelp())
		}
	}

	return nil
}

func integrationConnect(ctx context.Context, cmd *cli.Command) error {
	kind := strings.ToLower(cmd.Args().First())
	if kind == "" {
		return cli.Exit("usage: stagd integrations connect <kind> (see stagd integrations kinds)", 2)
	}

	values, err := parseValues(cmd.StringSlice("value"))
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	kinds, err := c.Integrations.ListIntegrationKinds(ctx, connect.NewRequest(&apiv1.ListIntegrationKindsRequest{}))
	if err != nil {
		return err
	}

	var found *apiv1.IntegrationKind

	for _, k := range kinds.Msg.GetKinds() {
		if k.GetId() == kind {
			found = k
		}
	}

	if found == nil {
		return cli.Exit(fmt.Sprintf("this Stagd server doesn't offer a %q integration (see stagd integrations kinds)", kind), 2)
	}

	if err := askIntegrationFields(ctx, found, values); err != nil {
		return err
	}

	name := cmd.String("name")
	if name == "" {
		name = found.GetId()
	}

	res, err := c.Integrations.CreateIntegration(ctx, connect.NewRequest(&apiv1.CreateIntegrationRequest{Kind: kind, Name: name, Values: values}))
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		return output.JSON(cmd.Root().Writer, res.Msg)
	}

	i := res.Msg.GetIntegration()

	_, err = fmt.Fprintf(cmd.Root().Writer, "Connected %s as %q (%s)\n", found.GetName(), i.GetName(), i.GetAccount())

	return err
}

// askIntegrationFields asks for required fields not given; scripts get an error naming them.
func askIntegrationFields(ctx context.Context, kind *apiv1.IntegrationKind, values map[string]string) error {
	var missing []*apiv1.IntegrationField

	for _, f := range kind.GetFields() {
		if f.GetRequired() && values[f.GetKey()] == "" {
			missing = append(missing, f)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	if !interactive() {
		names := make([]string, 0, len(missing))

		for _, f := range missing {
			names = append(names, f.GetKey())
		}

		return cli.Exit("missing --value for "+strings.Join(names, ", "), 2)
	}

	answers := make([]string, len(missing))
	fields := make([]huh.Field, 0, len(missing))

	for i, f := range missing {
		if f.GetMultiline() {
			fields = append(fields, huh.NewText().Title(f.GetLabel()).Description(f.GetHelp()).Value(&answers[i]))

			continue
		}

		input := huh.NewInput().Title(f.GetLabel()).Description(f.GetHelp()).Value(&answers[i])
		if f.GetSecret() {
			input = input.EchoMode(huh.EchoModePassword)
		}

		fields = append(fields, input)
	}

	if err := huh.NewForm(huh.NewGroup(fields...)).RunWithContext(ctx); err != nil {
		return err
	}

	for i, f := range missing {
		values[f.GetKey()] = answers[i]
	}

	return nil
}

func integrationList(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Integrations.ListIntegrations(ctx, connect.NewRequest(&apiv1.ListIntegrationsRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	rows := make([][]string, 0, len(res.Msg.GetIntegrations()))

	for _, i := range res.Msg.GetIntegrations() {
		status := label(i.GetStatus().String(), "INTEGRATION_STATUS_")
		if msg := i.GetStatusMessage(); msg != "" {
			status += ": " + msg
		}

		rows = append(rows, []string{i.GetName(), i.GetKind(), i.GetAccount(), status})
	}

	return output.Table(w, []string{"name", "kind", "account", "status"}, rows)
}

func integrationUpdate(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" || len(cmd.StringSlice("value")) == 0 {
		return cli.Exit("usage: stagd integrations update <name> --value key=value …", 2)
	}

	values, err := parseValues(cmd.StringSlice("value"))
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Integrations.UpdateIntegration(ctx, connect.NewRequest(&apiv1.UpdateIntegrationRequest{Integration: name, Values: values}))
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "Updated %s (%s)\n", res.Msg.GetIntegration().GetName(), res.Msg.GetIntegration().GetAccount())

	return err
}

func integrationTest(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Integrations.TestIntegration(ctx, connect.NewRequest(&apiv1.TestIntegrationRequest{Integration: cmd.Args().First()}))
	if err != nil {
		return err
	}

	i := res.Msg.GetIntegration()

	if i.GetStatus() == apiv1.IntegrationStatus_INTEGRATION_STATUS_FAILING {
		return cli.Exit(fmt.Sprintf("%s is failing: %s", i.GetName(), i.GetStatusMessage()), 1)
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "%s works (%s)\n", i.GetName(), i.GetAccount())

	return err
}

func integrationDelete(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return cli.Exit("usage: stagd integrations delete <name>", 2)
	}

	if !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("pass --yes to delete without a prompt", 2)
		}

		confirmed := false
		form := huh.NewForm(huh.NewGroup(huh.NewConfirm().
			Title(fmt.Sprintf("Delete %s?", name)).
			Description("Stagd forgets its credentials.").
			Affirmative("Delete").
			Negative("Cancel").
			Value(&confirmed)))

		if err := form.RunWithContext(ctx); err != nil {
			return err
		}

		if !confirmed {
			return nil
		}
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	if _, err := c.Integrations.DeleteIntegration(ctx, connect.NewRequest(&apiv1.DeleteIntegrationRequest{Integration: name})); err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "Deleted %s.\n", name)

	return err
}
