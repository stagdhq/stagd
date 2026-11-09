package cli

import (
	"context"
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
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
			value := "…"

			if len(f.GetOptions()) > 0 {
				names := make([]string, 0, len(f.GetOptions()))

				for _, o := range f.GetOptions() {
					names = append(names, o.GetValue())
				}

				value = strings.Join(names, "|")
			}

			when := ""
			if sw := f.GetShownWith(); sw != "" {
				when = fmt.Sprintf(" (with %s)", sw)
			}

			fmt.Fprintf(w, "  --value %s=%s%s  %s. %s\n", f.GetKey(), value, when, f.GetLabel(), f.GetHelp())

			for _, o := range f.GetOptions() {
				fmt.Fprintf(w, "      %s: %s. %s\n", o.GetValue(), o.GetLabel(), o.GetHelp())
			}
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

// choiceOf is a choice field's value: the one given, or its first option's.
func choiceOf(kind *apiv1.IntegrationKind, key string, values map[string]string) string {
	if v := values[key]; v != "" {
		return v
	}

	for _, f := range kind.GetFields() {
		if f.GetKey() == key && len(f.GetOptions()) > 0 {
			return f.GetOptions()[0].GetValue()
		}
	}

	return ""
}

// fieldShown says whether a field applies: always, or while the checkbox it belongs to is ticked, or ("key=value")
// while that option is chosen.
func fieldShown(kind *apiv1.IntegrationKind, f *apiv1.IntegrationField, values map[string]string) bool {
	key, option, choice := strings.Cut(f.GetShownWith(), "=")

	switch {
	case key == "":
		return true

	case choice:
		return choiceOf(kind, key, values) == option

	default:
		return values[key] == "true"
	}
}

// askIntegrationFields asks for choices and required fields not given, of those that apply; scripts get a choice's
// first option, and an error naming the required fields.
func askIntegrationFields(ctx context.Context, kind *apiv1.IntegrationKind, values map[string]string) error {
	// Choices first, one at a time: what else is asked for depends on them.
	for _, f := range kind.GetFields() {
		if len(f.GetOptions()) == 0 || values[f.GetKey()] != "" || !fieldShown(kind, f, values) || !interactive() {
			continue
		}

		options := make([]huh.Option[string], 0, len(f.GetOptions()))

		for _, o := range f.GetOptions() {
			options = append(options, huh.NewOption(o.GetLabel(), o.GetValue()))
		}

		var chosen string

		pick := huh.NewSelect[string]().Title(f.GetLabel()).Description(f.GetHelp()).Options(options...).Value(&chosen)
		if err := newForm(huh.NewGroup(pick)).RunWithContext(ctx); err != nil {
			return err
		}

		values[f.GetKey()] = chosen
	}

	var missing []*apiv1.IntegrationField

	for _, f := range kind.GetFields() {
		if f.GetRequired() && len(f.GetOptions()) == 0 && values[f.GetKey()] == "" && fieldShown(kind, f, values) {
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

	if err := newForm(huh.NewGroup(fields...)).RunWithContext(ctx); err != nil {
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

		if ok, err := confirm(cmd, fmt.Sprintf("Delete %s?", name), "Stagd forgets its credentials."); err != nil || !ok {
			return err
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
