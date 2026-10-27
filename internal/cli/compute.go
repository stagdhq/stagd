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

	"github.com/stagdhq/stagd/deploy/kubernetes"
	"github.com/stagdhq/stagd/internal/client/api"
	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
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
					&cli.BoolFlag{Name: "run", Usage: "docker: run its relay on this Docker host, and wait until it connects"},
					&cli.BoolFlag{Name: "print", Usage: "docker: print the docker commands that run the relay, with its token, instead"},
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
				Usage:     "install a connection's relay: a new token, and what to run",
				ArgsUsage: nameArg,
				Description: "For Kubernetes, prints everything the cluster needs, for kubectl:\n\n" +
					"  stagd compute install <name> | kubectl apply -f -\n\n" +
					"Stagd's access (its own stagd-* namespaces only), the relay with a new token, and what the\n" +
					"connection chooses (the builder, the Cloudflare Tunnel). Run it again after changing those.\n" +
					"For a Docker host, on the host itself, --run runs the relay (--print prints docker's commands\n" +
					"for it instead):\n\n" +
					"  stagd compute install <name> --run\n\n" +
					"Either way the old token stops working: a relay still running with it is cut off (with its\n" +
					"relay connected, pass --yes).",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "don't ask for confirmation when its relay is connected"},
					&cli.BoolFlag{Name: "wait", Usage: "wait until the relay connects"},
					&cli.BoolFlag{Name: "run", Usage: "docker: run the relay on this Docker host, in place of the one there, and wait until it connects"},
					&cli.BoolFlag{Name: "print", Usage: "docker: print the docker commands that run the relay, with its token, instead"},
				},
				Action: computeInstall,
			},
			{
				Name:      "uninstall",
				Usage:     "remove Stagd's install from your compute: kubernetes or docker",
				ArgsUsage: "kubernetes | docker",
				Description: "For a Kubernetes cluster, prints every manifest a Stagd install can bring, without its tokens:\n\n" +
					"  stagd compute uninstall kubernetes | kubectl delete --ignore-not-found -f -\n\n" +
					"removes Stagd's account and rights, the relay, cloudflared, the builder and registry. Run it\n" +
					"as a cluster admin, after deleting the connection in Stagd (which removes the Cloudflare\n" +
					"Tunnel Stagd made for it). Stagd deletes a connection only when no previews run on it.\n\n" +
					"For a Docker host, on the host itself, --run removes the relay (--print prints docker's\n" +
					"commands for it instead):\n\n" +
					"  stagd compute uninstall docker --run",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "run", Usage: "docker: remove the relay from this Docker host"},
					&cli.BoolFlag{Name: "print", Usage: "docker: print the docker commands that remove it instead"},
				},
				Action: computeUninstall,
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

	if err := newForm(huh.NewGroup(fields...)).RunWithContext(ctx); err != nil {
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

	if (cmd.Bool("run") || cmd.Bool("print")) && provider.GetProvider() != apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER {
		return cli.Exit("--run and --print are for a Docker host: "+runElsewhere(provider.GetProvider()), 2)
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

	if token := created.Msg.GetRelayToken(); token != "" && cmd.Bool("run") {
		fmt.Fprintf(w, "Created %s as %q.\n", provider.GetName(), conn.GetName())

		if err := runDockerRelay(ctx, w, cmd.String("api-url"), conn.GetId(), token, relayImage(cmd.Root().Version), created.Msg.GetTunnelToken()); err != nil {
			return fmt.Errorf("%w\nThe connection is made: run its relay with `stagd compute install %s --run`", err, conn.GetName())
		}

		if err := waitForRelay(ctx, w, c, conn); err != nil {
			return err
		}
	} else if token != "" {
		fmt.Fprintf(w, "Created %s as %q; it works once its relay connects.\n\n", provider.GetName(), conn.GetName())
		fmt.Fprint(w, relayInstructions(provider.GetProvider(), cmd.String("api-url"), token, conn.GetName(), relayImage(cmd.Root().Version), cmd.Bool("print"), created.Msg.GetTunnelToken()))

		// A cluster's install is its own command, which waits itself.
		if cmd.Bool("wait") && provider.GetProvider() != apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES {
			if err := waitForRelay(ctx, w, c, conn); err != nil {
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

		rows = append(rows, []string{marker, conn.GetName(), computeLabel(conn.GetProvider()), conn.GetAccount(), relayVersion(conn.GetRelay()), status})
	}

	return output.Table(w, []string{"", "name", "provider", "account", "relay", "status"}, rows)
}

// relayVersion is a connected relay's release, and the newer one when
// there is: `stagd compute install <name>` updates it.
func relayVersion(r *apiv1.StagdRelay) string {
	switch {
	case r == nil:
		return ""

	case r.GetLatestVersion() != "":
		return fmt.Sprintf("%s (%s is out)", r.GetVersion(), r.GetLatestVersion())
	}

	return r.GetVersion()
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

		if ok, err := confirm(cmd, fmt.Sprintf("Delete %s?", conn.GetName()), "Stagd forgets it and its credentials; while previews still run on it, it can't be deleted. What it installed on the compute stays, for you to remove."); err != nil || !ok {
			return err
		}
	}

	// Its relay on this host, if it runs here: asked about once it's deleted.
	relayHere := false

	if conn.GetProvider() == apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER {
		found, owner := relayOwner(ctx)
		relayHere = found && owner == conn.GetId()
	}

	if _, err := c.Compute.DeleteComputeConnection(ctx, connect.NewRequest(&apiv1.DeleteComputeConnectionRequest{Id: conn.GetId()})); err != nil {
		return err
	}

	w := cmd.Root().Writer

	if relayHere && !cmd.Bool("yes") {
		fmt.Fprintf(w, "Deleted %s.\n", conn.GetName())

		if ok, err := confirm(cmd, "Remove its relay from this Docker host too?", fmt.Sprintf("Its relay still runs here, as the container %s.", relayContainer)); err != nil || !ok {
			if err == nil {
				_, err = fmt.Fprint(w, cleanup(conn.GetProvider()))
			}

			return err
		}

		return removeDockerRelay(ctx, w)
	}

	_, err = fmt.Fprintf(w, "Deleted %s.\n%s", conn.GetName(), cleanup(conn.GetProvider()))

	return err
}

// The commands that remove Stagd's install from a cluster, cluster-wide
// objects included: Stagd can't, its account only manages stagd-* namespaces.
const kubeCleanup = "  stagd compute uninstall kubernetes | kubectl delete --ignore-not-found -f -\n"

// cleanup is what a deleted connection left on its compute, and how to
// remove it; Stagd has already removed what it made elsewhere (a cluster's
// Cloudflare Tunnel).
func cleanup(provider apiv1.ComputeProvider) string {
	switch provider {
	case apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES:
		return "\nStagd's install is still in the cluster: the relay, cloudflared, the builder and registry, and\n" +
			"Stagd's account and its rights. To remove them, as a cluster admin:\n\n" + kubeCleanup

	case apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER:
		return "\nThe relay still runs on the Docker host. To remove it, there:\n\n  stagd compute uninstall docker --run\n"
	}

	return ""
}

// What removes a Docker host's relay, and its network once nothing else is on it.
const dockerCleanup = "  docker rm -f " + relayContainer + " " + tunnelContainer + "\n  docker network rm " + relayNetwork + "\n"

// runElsewhere is what to run instead of --run, for a provider without it.
func runElsewhere(provider apiv1.ComputeProvider) string {
	if provider == apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES {
		return "for a cluster, pipe the install to kubectl (stagd compute install <name> | kubectl apply -f -)"
	}

	return "this compute has no relay to run"
}

// relayInstructions is how to run a connection's relay next to its compute:
// for a cluster, the install command to pipe to kubectl; for a Docker host,
// the CLI's --run there, or docker's commands with the token (--print).
func relayInstructions(provider apiv1.ComputeProvider, stagdURL, token, name, image string, print bool, tunnelToken string) string {
	if provider == apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES {
		return "Install it in the cluster, with kubectl's access to it:\n\n" +
			"  stagd compute install " + name + " | kubectl apply -f -\n\n" +
			"Stagd's access (its own stagd-* namespaces only), the relay and its token. --wait waits for\n" +
			"the relay to connect; `stagd compute list` shows it too.\n"
	}

	if provider == apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER && print {
		return "Run the relay on the Docker host:\n\n" +
			dockerRunCommand(stagdURL, token, image, tunnelToken) + "\n" +
			"The tokens are only shown now. `stagd compute list` shows when the relay is connected.\n"
	}

	if provider == apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER {
		return "Run the relay on the Docker host, with the CLI there signed in:\n\n" +
			"  stagd compute install " + name + " --run\n\n" +
			"It gives the relay a new token and waits until it connects.\n"
	}

	return ""
}

// dockerRunCommand is the relay's docker run, to copy: its own network
// first, then the relay on it, as --run does.
func dockerRunCommand(stagdURL, token, image, tunnelToken string) string {
	relayURL, onHost := relayStagdURL(stagdURL)
	addHost, tunnelPort := "", ""

	if onHost {
		addHost = "    --add-host " + dockerHostName + ":host-gateway \\\n"
	}

	if tunnelToken != "" {
		tunnelPort = "    -e STAGD_RELAY_HTTP=" + relayTunnelPort + " \\\n"
	}

	out := "  docker network create " + relayNetwork + "\n" +
		"  docker run -d --name " + relayContainer + " --restart unless-stopped \\\n" +
		"    --network " + relayNetwork + " --user root \\\n" +
		"    -v /var/run/docker.sock:/var/run/docker.sock \\\n" +
		addHost + tunnelPort +
		"    -e STAGD_URL=" + relayURL + " \\\n" +
		"    -e STAGD_RELAY_TOKEN=" + token + " \\\n" +
		"    " + image + " relay\n"

	// cloudflared beside it, for the connection's Cloudflare Tunnel.
	if tunnelToken != "" {
		out += "  docker run -d --name " + tunnelContainer + " --restart unless-stopped \\\n" +
			"    --network " + relayNetwork + " \\\n" +
			"    -e TUNNEL_TOKEN=" + tunnelToken + " \\\n" +
			"    " + tunnelImage + " tunnel --no-autoupdate run\n"
	}

	return out
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
func waitForRelay(ctx context.Context, w io.Writer, c *api.Clients, relayed *apiv1.ComputeConnection) error {
	id := relayed.GetId()
	logs := "kubectl -n stagd-system logs deploy/stagd-relay"

	if relayed.GetProvider() == apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER {
		logs = "docker logs " + relayContainer
	}

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
			return fmt.Errorf("the relay didn't connect within %s: check it's running (%s)", relayWait, logs)

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

	run, print := cmd.Bool("run"), cmd.Bool("print")
	docker := conn.GetProvider() == apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER

	if (run || print) && !docker {
		return cli.Exit("--run and --print are for a Docker host: "+runElsewhere(conn.GetProvider()), 2)
	}

	// A Docker host's relay is run here, or its commands printed: which, before a new token.
	if docker && !run && !print && !cmd.Bool("json") {
		return cli.Exit("on the Docker host, --run runs the relay; --print prints docker's commands for it instead", 2)
	}

	// A relay running now is cut off: ask first. One run here is replaced at once.
	if conn.GetStatus() == apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_ACTIVE && !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("its relay is connected: pass --yes to give it a new token anyway", 2)
		}

		detail := "The relay running now is cut off until the new install runs."

		if run {
			detail = "The relay running now is cut off; the one started here takes over."
		}

		if ok, err := confirm(cmd, fmt.Sprintf("A new relay token for %s?", conn.GetName()), detail); err != nil || !ok {
			return err
		}
	}

	w := cmd.Root().Writer

	// A cluster: its manifest on stdout, for kubectl; anything else on stderr.
	if conn.GetProvider() == apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES {
		res, err := c.Compute.CreateInstallManifest(ctx, connect.NewRequest(&apiv1.CreateInstallManifestRequest{Id: conn.GetId()}))
		if err != nil {
			return err
		}

		if _, err := io.WriteString(w, res.Msg.GetManifest()); err != nil {
			return err
		}

		if !cmd.Bool("wait") {
			return nil
		}

		// kubectl applies once its input ends: end it, then wait.
		if f, ok := w.(*os.File); ok {
			f.Close()
		}

		return waitForRelay(ctx, cmd.Root().ErrWriter, c, conn)
	}

	res, err := c.Compute.CreateRelayInstall(ctx, connect.NewRequest(&apiv1.CreateRelayInstallRequest{Id: conn.GetId()}))
	if err != nil {
		return err
	}

	if run {
		fmt.Fprintf(w, "A new relay token for %s; the old one no longer works.\n", conn.GetName())

		if err := runDockerRelay(ctx, w, cmd.String("api-url"), conn.GetId(), res.Msg.GetRelayToken(), relayImage(cmd.Root().Version), res.Msg.GetTunnelToken()); err != nil {
			return err
		}

		return waitForRelay(ctx, w, c, conn)
	}

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	fmt.Fprintf(w, "A new relay token for %s; the old one no longer works.\n\n", conn.GetName())
	fmt.Fprint(w, relayInstructions(conn.GetProvider(), cmd.String("api-url"), res.Msg.GetRelayToken(), conn.GetName(), relayImage(cmd.Root().Version), true, res.Msg.GetTunnelToken()))

	if cmd.Bool("wait") {
		return waitForRelay(ctx, w, c, conn)
	}

	return nil
}

// computeUninstall prints every manifest an install can bring, for kubectl
// delete, or removes a Docker host's relay; it needs no connection, so it
// works after Stagd forgets it.
func computeUninstall(ctx context.Context, cmd *cli.Command) error {
	w := cmd.Root().Writer

	switch kind := strings.ToLower(cmd.Args().First()); kind {
	case "kubernetes":
		if cmd.Bool("run") || cmd.Bool("print") {
			return cli.Exit("--run and --print are for a Docker host: for a cluster, pipe this to kubectl delete --ignore-not-found -f -", 2)
		}

	case "docker":
		switch {
		case cmd.Bool("run"):
			return removeDockerRelay(ctx, w)

		case cmd.Bool("print"):
			_, err := io.WriteString(w, dockerCleanup)

			return err
		}

		return cli.Exit("on the Docker host, --run removes the relay; --print prints docker's commands for it instead", 2)

	case "":
		return cli.Exit("usage: stagd compute uninstall kubernetes | kubectl delete --ignore-not-found -f -, or stagd compute uninstall docker --run", 2)

	default:
		return cli.Exit(fmt.Sprintf("nothing to uninstall for %q: Stagd removes what it made there itself (only kubernetes and docker have an install to remove)", kind), 2)
	}

	for i, name := range kubernetes.Uninstall {
		raw, err := kubernetes.Files.ReadFile(name)
		if err != nil {
			return err
		}

		if i > 0 {
			io.WriteString(w, "---\n")
		}

		if _, err := w.Write(raw); err != nil {
			return err
		}
	}

	fmt.Fprintln(cmd.Root().ErrWriter, "Preview namespaces left behind, if any: kubectl delete namespaces -l stagd.dev/managed=true")

	return nil
}
