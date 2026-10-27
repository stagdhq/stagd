package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// A Docker host's relay, run by the CLI with --run: the docker command,
// with whatever context it has (Docker Desktop, OrbStack, a remote host).
// It runs as the container stagd-relay on a network of its own: on
// Docker's default bridge, it keeps the host's DNS after joining a
// preview's network, and can't resolve the preview's services. When Stagd
// reaches the host's previews through a Cloudflare Tunnel, cloudflared runs
// beside it as stagd-tunnel, sending preview traffic to the relay's port
// for it (:8081); Stagd's own traffic stays on the relay's connection.

const (
	relayContainer  = "stagd-relay"
	relayNetwork    = "stagd-relay"
	tunnelContainer = "stagd-tunnel"

	// cloudflared's image: Cloudflare supports releases from about the last year, so keep it current.
	tunnelImage = "cloudflare/cloudflared:2026.10.4"

	// Where the relay takes preview traffic from the tunnel (Stagd's tunnel sends it to stagd-relay:8081).
	relayTunnelPort = ":8081"

	// Which connection a relay run by the CLI belongs to.
	relayConnectionLabel = "dev.stagd.connection"

	// Where a relay in a container finds a Stagd running on the host itself.
	dockerHostName = "host.docker.internal"
)

// docker runs the docker command with extra environment, its output (and
// what it said on stderr, in its error); replaced in tests.
var docker = func(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), env...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("--run needs the docker command on this host")
		}

		return stdout.String(), fmt.Errorf("docker %s: %s", args[0], strings.TrimSpace(firstNonEmpty(stderr.String(), err.Error())))
	}

	return stdout.String(), nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}

	return ""
}

// relayStagdURL is Stagd's address as a relay in a container reaches it:
// a Stagd on this host's localhost is the host's, not the container's.
func relayStagdURL(stagdURL string) (string, bool) {
	u, err := url.Parse(stagdURL)
	if err != nil {
		return stagdURL, false
	}

	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		host := dockerHostName

		if port := u.Port(); port != "" {
			host += ":" + port
		}

		u.Host = host

		return u.String(), true
	}

	return stagdURL, false
}

// dockerRunArgs is docker run for the relay, without the token's value:
// docker reads STAGD_RELAY_TOKEN from its own environment, so the token
// isn't in the command line. With a tunnel, it takes preview traffic from
// cloudflared too.
func dockerRunArgs(stagdURL, connectionID, image string, tunnel bool) []string {
	relayURL, onHost := relayStagdURL(stagdURL)

	args := []string{
		"run", "-d", "--name", relayContainer, "--restart", "unless-stopped",
		"--network", relayNetwork, "--user", "root",
		"--label", relayConnectionLabel + "=" + connectionID,
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
	}

	// Docker Desktop and OrbStack know the host's name; on Linux it's added.
	if onHost {
		args = append(args, "--add-host", dockerHostName+":host-gateway")
	}

	if tunnel {
		args = append(args, "-e", "STAGD_RELAY_HTTP="+relayTunnelPort)
	}

	return append(args, "-e", "STAGD_URL="+relayURL, "-e", "STAGD_RELAY_TOKEN", image, "relay")
}

// tunnelRunArgs is docker run for cloudflared, the tunnel's token in docker's environment only.
func tunnelRunArgs(connectionID string) []string {
	return []string{
		"run", "-d", "--name", tunnelContainer, "--restart", "unless-stopped",
		"--network", relayNetwork,
		"--label", relayConnectionLabel + "=" + connectionID,
		"-e", "TUNNEL_TOKEN",
		tunnelImage, "tunnel", "--no-autoupdate", "run",
	}
}

// relayOwner is which connection the stagd-relay container on this host
// runs for, from its label or else its token (stagd_relay.<org>.<connection>.<secret>);
// "" without one, or without Docker.
func relayOwner(ctx context.Context) (found bool, connectionID string) {
	out, err := docker(ctx, nil, "inspect", "--format", `{{index .Config.Labels "`+relayConnectionLabel+`"}}{{"\n"}}{{range .Config.Env}}{{println .}}{{end}}`, relayContainer)
	if err != nil {
		return false, ""
	}

	lines := strings.Split(out, "\n")

	if id := strings.TrimSpace(lines[0]); id != "" {
		return true, id
	}

	for _, line := range lines[1:] {
		if token, ok := strings.CutPrefix(line, "STAGD_RELAY_TOKEN=stagd_relay."); ok {
			if parts := strings.Split(token, "."); len(parts) == 3 {
				return true, parts[1]
			}
		}
	}

	return true, ""
}

// runDockerRelay starts the connection's relay on this host with its new
// token: the network if it's missing, and the relay in place of one for
// the same connection. A relay for another connection stays. With a tunnel
// token, cloudflared runs beside it; without, one left from before goes.
func runDockerRelay(ctx context.Context, w io.Writer, stagdURL, connectionID, token, image, tunnelToken string) error {
	if found, owner := relayOwner(ctx); found {
		if owner != connectionID {
			return fmt.Errorf("a relay for another connection runs here as %s: remove it first with `stagd compute uninstall docker --run`", relayContainer)
		}

		if _, err := docker(ctx, nil, "rm", "-f", relayContainer); err != nil {
			return err
		}
	}

	if _, err := docker(ctx, nil, "network", "inspect", relayNetwork); err != nil {
		if _, err := docker(ctx, nil, "network", "create", relayNetwork); err != nil {
			return err
		}
	}

	if _, err := docker(ctx, nil, "pull", "--quiet", image); err != nil {
		return err
	}

	if _, err := docker(ctx, []string{"STAGD_RELAY_TOKEN=" + token}, dockerRunArgs(stagdURL, connectionID, image, tunnelToken != "")...); err != nil {
		return err
	}

	fmt.Fprintf(w, "Started the relay on this Docker host, as the container %s (%s).\n", relayContainer, image)

	// cloudflared, anew: its token may have changed with the tunnel.
	if _, err := docker(ctx, nil, "rm", "-f", tunnelContainer); err != nil && !strings.Contains(err.Error(), "No such container") {
		return err
	}

	if tunnelToken == "" {
		return nil
	}

	if _, err := docker(ctx, nil, "pull", "--quiet", tunnelImage); err != nil {
		return err
	}

	if _, err := docker(ctx, []string{"TUNNEL_TOKEN=" + tunnelToken}, tunnelRunArgs(connectionID)...); err != nil {
		return err
	}

	_, err := fmt.Fprintf(w, "Started cloudflared beside it, as the container %s: previews are reached through the connection's Cloudflare Tunnel.\n", tunnelContainer)

	return err
}

// removeDockerRelay removes the relay on this host, cloudflared beside it,
// and their network once nothing else is on it; nothing to remove isn't an
// error.
func removeDockerRelay(ctx context.Context, w io.Writer) error {
	found, _ := relayOwner(ctx)

	if found {
		if _, err := docker(ctx, nil, "rm", "-f", relayContainer); err != nil {
			return err
		}
	}

	if _, err := docker(ctx, nil, "rm", "-f", tunnelContainer); err != nil && !strings.Contains(err.Error(), "No such container") {
		return err
	}

	// In use by something else: it stays.
	networkGone := false

	if _, err := docker(ctx, nil, "network", "inspect", relayNetwork); err == nil {
		_, err := docker(ctx, nil, "network", "rm", relayNetwork)
		networkGone = err == nil
	}

	switch {
	case found && networkGone:
		fmt.Fprintf(w, "Removed the relay (%s) and its network from this Docker host.\n", relayContainer)

	case found:
		fmt.Fprintf(w, "Removed the relay (%s) from this Docker host.\n", relayContainer)

	default:
		fmt.Fprintln(w, "No relay runs on this Docker host.")
	}

	return nil
}
