package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/huh/v2"
	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	"stagd.dev/stagd/internal/client/api"
	apiv1 "stagd.dev/stagd/internal/gen/stagd/api/v1"
	"stagd.dev/stagd/internal/output"
)

// stdin is where `--value key=@-` reads from; replaced in tests.
var stdin io.Reader = os.Stdin

func computeCommand() *cli.Command {
	nameArg := "<name or id>"

	return &cli.Command{
		Name:  "compute",
		Usage: "the compute your previews run on",
		Commands: []*cli.Command{
			{
				Name:   "providers",
				Usage:  "list the kinds of compute you can connect",
				Action: computeProviders,
			},
			{
				Name:      "connect",
				Usage:     "connect a compute account to your org",
				ArgsUsage: "<provider>",
				Description: "Asks for each value the provider needs. In scripts, pass them with --value key=value;\n" +
					"use key=@- to read a secret from stdin instead of leaving it in your shell history.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Usage: "a name for the connection (default: the provider's name)"},
					&cli.StringSliceFlag{Name: "value", Usage: "a field value, key=value or key=@- (stdin)"},
					&cli.BoolFlag{Name: "wait", Usage: "through a relay: wait until it connects"},
				},
				Action: computeConnect,
			},
			{
				Name:   "list",
				Usage:  "list your org's compute connections",
				Action: computeList,
			},
			{
				Name:      "test",
				Usage:     "check a connection's credentials again",
				ArgsUsage: nameArg,
				Action:    computeTest,
			},
			{
				Name:      "update",
				Usage:     "change a connection's settings, or give it a new secret",
				ArgsUsage: nameArg,
				Description: "Only the values given change; a secret not given stays as it was. Stagd checks the result\n" +
					"with the provider before saving it. For a new token: --value token=@- reads it from stdin.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "value", Usage: "a field value, key=value or key=@- (stdin)"},
				},
				Action: computeUpdate,
			},
			{
				Name:      "install",
				Usage:     "install a connection's relay again: a new token, and how to run it",
				ArgsUsage: nameArg,
				Description: "For a connection through stagd relay whose install command ran out, or a relay to move.\n" +
					"The old token stops working: a relay still running with it is cut off.",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "don't ask for confirmation when its relay is connected"},
					&cli.BoolFlag{Name: "wait", Usage: "wait until the relay connects"},
				},
				Action: computeInstall,
			},
			{
				Name:      "default",
				Usage:     "run new previews on this connection",
				ArgsUsage: nameArg,
				Action:    computeDefault,
			},
			{
				Name:      "delete",
				Usage:     "forget a connection and its credentials",
				ArgsUsage: nameArg,
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "don't ask for confirmation"},
				},
				Action: computeDelete,
			},
		},
	}
}

func computeLabel(p apiv1.ComputeProvider) string {
	return strings.ReplaceAll(label(p.String(), "COMPUTE_PROVIDER_"), "_", "-")
}

func computeProviders(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Compute.ListProviders(ctx, connect.NewRequest(&apiv1.ListProvidersRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	rows := make([][]string, 0, len(res.Msg.GetProviders()))

	for _, p := range res.Msg.GetProviders() {
		caps := p.GetCapabilities()
		rows = append(rows, []string{computeLabel(p.GetProvider()), p.GetDescription(), yesNo(caps.GetSleep()), yesNo(caps.GetVolumeSnapshots()), strings.Join(caps.GetRegions(), ", ")})
	}

	return output.Table(w, []string{"provider", "description", "sleep", "snapshots", "regions"}, rows)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}

	return "no"
}

// parseValues reads --value key=value flags; key=@- reads the value from stdin.
func parseValues(flags []string) (map[string]string, error) {
	values := map[string]string{}
	readStdin := false

	for _, f := range flags {
		key, value, ok := strings.Cut(f, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("--value %q: use key=value", f)
		}

		if value == "@-" {
			if readStdin {
				return nil, errors.New("only one --value can read from stdin")
			}

			readStdin = true
			line, err := bufio.NewReader(stdin).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}

			value = strings.TrimRight(line, "\r\n")
		}

		values[key] = value
	}

	return values, nil
}

// askMissing prompts for the fields not given with --value; scripts get an error naming them instead.
func askMissing(ctx context.Context, p *apiv1.ProviderInfo, values map[string]string) error {
	var missing []*apiv1.ProviderField

	for _, f := range p.GetFields() {
		if _, ok := values[f.GetKey()]; !ok {
			missing = append(missing, f)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	if !interactive() {
		var required []string

		for _, f := range missing {
			if f.GetRequired() {
				required = append(required, f.GetKey())
			}
		}

		if len(required) > 0 {
			return cli.Exit("missing --value for: "+strings.Join(required, ", "), 2)
		}

		return nil
	}

	answers := make(map[string]*string, len(missing))
	fields := make([]huh.Field, 0, len(missing))

	for _, f := range missing {
		v := ""
		answers[f.GetKey()] = &v

		if len(f.GetChoices()) > 0 {
			fields = append(fields, huh.NewSelect[string]().Title(f.GetLabel()).Description(f.GetHelp()).Options(huh.NewOptions(f.GetChoices()...)...).Value(&v))

			continue
		}

		input := huh.NewInput().Title(f.GetLabel()).Description(f.GetHelp()).Value(&v)

		if f.GetSecret() {
			input = input.EchoMode(huh.EchoModePassword)
		}

		if f.GetRequired() {
			label := f.GetLabel()
			input = input.Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("%s is required", label)
				}

				return nil
			})
		}

		fields = append(fields, input)
	}

	if err := huh.NewForm(huh.NewGroup(fields...)).RunWithContext(ctx); err != nil {
		return err
	}

	for key, v := range answers {
		if *v != "" {
			values[key] = *v
		}
	}

	return nil
}

func computeConnect(ctx context.Context, cmd *cli.Command) error {
	want := strings.ToLower(strings.TrimSpace(cmd.Args().First()))
	if want == "" {
		return cli.Exit("usage: stagd compute connect <provider> (see stagd compute providers)", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Compute.ListProviders(ctx, connect.NewRequest(&apiv1.ListProvidersRequest{}))
	if err != nil {
		return err
	}

	var provider *apiv1.ProviderInfo

	for _, p := range res.Msg.GetProviders() {
		if computeLabel(p.GetProvider()) == want {
			provider = p
		}
	}

	if provider == nil {
		return cli.Exit(fmt.Sprintf("this Stagd server doesn't offer %q compute (see stagd compute providers)", want), 2)
	}

	values, err := parseValues(cmd.StringSlice("value"))
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	if err := askMissing(ctx, provider, values); err != nil {
		return err
	}

	name := cmd.String("name")
	if name == "" {
		name = computeLabel(provider.GetProvider())
	}

	created, err := c.Compute.CreateComputeConnection(ctx, connect.NewRequest(&apiv1.CreateComputeConnectionRequest{
		Provider: provider.GetProvider(),
		Name:     name,
		Values:   values,
	}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer
	conn := created.Msg.GetConnection()

	if cmd.Bool("json") {
		return output.JSON(w, created.Msg)
	}

	if token := created.Msg.GetRelayToken(); token != "" {
		fmt.Fprintf(w, "Created %s as %q; it works once its relay connects.\n\n", provider.GetName(), conn.GetName())
		fmt.Fprint(w, relayInstructions(provider.GetProvider(), cmd.String("api-url"), token, created.Msg.GetRelayInstallUrl(), relayImage(cmd.Root().Version)))

		if cmd.Bool("wait") {
			if err := waitForRelay(ctx, cmd, c, conn.GetId()); err != nil {
				return err
			}
		}
	} else {
		fmt.Fprintf(w, "Connected %s (%s) as %q\n", provider.GetName(), conn.GetAccount(), conn.GetName())
	}

	if conn.GetDefault() {
		fmt.Fprintln(w, "New previews run on it.")
	} else {
		fmt.Fprintf(w, "Make it the default with `stagd compute default %s`\n", conn.GetName())
	}

	return nil
}

func computeList(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Compute.ListComputeConnections(ctx, connect.NewRequest(&apiv1.ListComputeConnectionsRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	rows := make([][]string, 0, len(res.Msg.GetConnections()))

	for _, conn := range res.Msg.GetConnections() {
		marker := ""
		if conn.GetDefault() {
			marker = "*"
		}

		status := label(conn.GetStatus().String(), "COMPUTE_CONNECTION_STATUS_")
		if msg := conn.GetStatusMessage(); msg != "" {
			status += ": " + msg
		}

		rows = append(rows, []string{marker, conn.GetName(), computeLabel(conn.GetProvider()), conn.GetAccount(), status})
	}

	return output.Table(w, []string{"", "name", "provider", "account", "status"}, rows)
}

// resolveConnection finds a connection by name or id.
func resolveConnection(ctx context.Context, c *api.Clients, ref string) (*apiv1.ComputeConnection, error) {
	if ref == "" {
		return nil, cli.Exit("name the connection (see stagd compute list)", 2)
	}

	res, err := c.Compute.ListComputeConnections(ctx, connect.NewRequest(&apiv1.ListComputeConnectionsRequest{}))
	if err != nil {
		return nil, err
	}

	for _, conn := range res.Msg.GetConnections() {
		if conn.GetName() == ref || conn.GetId() == ref {
			return conn, nil
		}
	}

	return nil, fmt.Errorf("no compute connection called %q (see stagd compute list)", ref)
}

func computeTest(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	conn, err := resolveConnection(ctx, c, cmd.Args().First())
	if err != nil {
		return err
	}

	res, err := c.Compute.TestComputeConnection(ctx, connect.NewRequest(&apiv1.TestComputeConnectionRequest{Id: conn.GetId()}))
	if err != nil {
		return err
	}

	tested := res.Msg.GetConnection()

	if tested.GetStatus() != apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_ACTIVE {
		return fmt.Errorf("%s doesn't work: %s", tested.GetName(), tested.GetStatusMessage())
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "%s works (%s)\n", tested.GetName(), tested.GetAccount())

	return err
}

func computeUpdate(ctx context.Context, cmd *cli.Command) error {
	if len(cmd.StringSlice("value")) == 0 {
		return cli.Exit("usage: stagd compute update <name> --value key=value …", 2)
	}

	values, err := parseValues(cmd.StringSlice("value"))
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	conn, err := resolveConnection(ctx, c, cmd.Args().First())
	if err != nil {
		return err
	}

	res, err := c.Compute.UpdateComputeConnection(ctx, connect.NewRequest(&apiv1.UpdateComputeConnectionRequest{Id: conn.GetId(), Values: values}))
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "Updated %s (%s)\n", res.Msg.GetConnection().GetName(), res.Msg.GetConnection().GetAccount())

	return err
}

func computeDefault(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	conn, err := resolveConnection(ctx, c, cmd.Args().First())
	if err != nil {
		return err
	}

	if _, err := c.Compute.SetDefaultComputeConnection(ctx, connect.NewRequest(&apiv1.SetDefaultComputeConnectionRequest{Id: conn.GetId()})); err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "New previews run on %s\n", conn.GetName())

	return err
}

func computeDelete(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	conn, err := resolveConnection(ctx, c, cmd.Args().First())
	if err != nil {
		return err
	}

	if !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("pass --yes to delete without a prompt", 2)
		}

		confirmed := false
		form := huh.NewForm(huh.NewGroup(huh.NewConfirm().
			Title(fmt.Sprintf("Delete %s?", conn.GetName())).
			Description("Stagd forgets its credentials. Previews running on it keep running until they're torn down.").
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

	if _, err := c.Compute.DeleteComputeConnection(ctx, connect.NewRequest(&apiv1.DeleteComputeConnectionRequest{Id: conn.GetId()})); err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "Deleted %s.\n", conn.GetName())

	return err
}

// relayInstructions is how to run a connection's relay next to its compute.
// installURL is the server's one-command install for Kubernetes; without it
// (an older server), the steps one by one.
func relayInstructions(provider apiv1.ComputeProvider, stagdURL, token, installURL, image string) string {
	const manifests = "https://raw.githubusercontent.com/stagdhq/stagd/main/deploy/kubernetes/"

	if provider == apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER {
		return "Run the relay on the Docker host:\n\n" +
			"  docker run -d --name stagd-relay --restart unless-stopped \\\n" +
			"    -v /var/run/docker.sock:/var/run/docker.sock \\\n" +
			"    -e STAGD_URL=" + stagdURL + " \\\n" +
			"    -e STAGD_RELAY_TOKEN=" + token + " \\\n" +
			"    " + image + " relay\n\n" +
			"The token is only shown now. `stagd compute list` shows when the relay is connected.\n"
	}

	if installURL != "" {
		return "Install it in the cluster: Stagd's access (its own stagd-* namespaces only), the relay and its token:\n\n" +
			"  kubectl apply -f " + installURL + "\n\n" +
			"The link works for an hour, or until the relay connects; running it again is fine.\n" +
			"`stagd compute list` shows when the relay is connected; `stagd compute install` makes a new command.\n"
	}

	return "Run the relay in the cluster:\n\n" +
		"  kubectl apply -f " + manifests + "stagd-access.yaml\n" +
		"  kubectl -n stagd-system create secret generic stagd-relay \\\n" +
		"    --from-literal=url=" + stagdURL + " \\\n" +
		"    --from-literal=token=" + token + "\n" +
		"  kubectl apply -f " + manifests + "stagd-relay.yaml\n\n" +
		"The token is only shown now. `stagd compute list` shows when the relay is connected.\n"
}

// relayImage is the relay's image for this CLI's version: a release runs
// its own, anything else the latest release.
func relayImage(version string) string {
	if v := strings.TrimPrefix(version, "v"); regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(v) {
		return "ghcr.io/stagdhq/stagd:" + v
	}

	return "ghcr.io/stagdhq/stagd:latest"
}

// relayWait is how long --wait waits for a relay to connect.
var relayWait = 10 * time.Minute

// waitForRelay waits until a relay connection's relay has connected and the
// connection works.
func waitForRelay(ctx context.Context, cmd *cli.Command, c *api.Clients, id string) error {
	w := cmd.Root().Writer
	fmt.Fprintln(w, "\nWaiting for the relay to connect…")

	ctx, cancel := context.WithTimeout(ctx, relayWait)
	defer cancel()

	for {
		res, err := c.Compute.ListComputeConnections(ctx, connect.NewRequest(&apiv1.ListComputeConnectionsRequest{}))
		if err != nil && ctx.Err() == nil {
			return err
		}

		for _, conn := range res.Msg.GetConnections() {
			if conn.GetId() == id && conn.GetStatus() == apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_ACTIVE {
				_, err := fmt.Fprintf(w, "Connected: %s\n", conn.GetAccount())

				return err
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("the relay didn't connect within %s: check it's running (kubectl -n stagd-system logs deploy/stagd-relay)", relayWait)

		case <-time.After(relayPoll):
		}
	}
}

var relayPoll = 2 * time.Second

func computeInstall(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	conn, err := resolveConnection(ctx, c, cmd.Args().First())
	if err != nil {
		return err
	}

	// A relay running now is cut off: ask first.
	if conn.GetStatus() == apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_ACTIVE && !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("its relay is connected: pass --yes to give it a new token anyway", 2)
		}

		confirmed := false
		form := huh.NewForm(huh.NewGroup(huh.NewConfirm().
			Title(fmt.Sprintf("A new relay token for %s?", conn.GetName())).
			Description("The relay running now is cut off until the new install runs.").
			Affirmative("New token").
			Negative("Cancel").
			Value(&confirmed)))

		if err := form.RunWithContext(ctx); err != nil {
			return err
		}

		if !confirmed {
			return nil
		}
	}

	res, err := c.Compute.CreateRelayInstall(ctx, connect.NewRequest(&apiv1.CreateRelayInstallRequest{Id: conn.GetId()}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	fmt.Fprintf(w, "A new relay token for %s; the old one no longer works.\n\n", conn.GetName())
	fmt.Fprint(w, relayInstructions(conn.GetProvider(), cmd.String("api-url"), res.Msg.GetRelayToken(), res.Msg.GetRelayInstallUrl(), relayImage(cmd.Root().Version)))

	if cmd.Bool("wait") {
		return waitForRelay(ctx, cmd, c, conn.GetId())
	}

	return nil
}
