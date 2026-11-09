package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeDocker is a Docker host as the docker command sees it: whether the
// relay's network is there, and which connection's relay runs, if one does.
type fakeDocker struct {
	network bool
	owner   string // the relay container's label; "" without one
	tunnel  bool   // whether cloudflared runs beside it
	token   string // the relay container's STAGD_RELAY_TOKEN
	tunnelT string // cloudflared's TUNNEL_TOKEN
	calls   [][]string
	envs    [][]string
}

func (d *fakeDocker) install(t *testing.T) {
	t.Helper()

	before := docker
	docker = func(_ context.Context, env []string, args ...string) (string, error) {
		d.calls = append(d.calls, args)
		d.envs = append(d.envs, env)

		switch {
		case args[0] == "inspect" && slices.Contains(args, tunnelContainer):
			if !d.tunnel {
				return "", errors.New("docker inspect: No such object: stagd-tunnel")
			}

			return "PATH=/usr/bin\nTUNNEL_TOKEN=" + d.tunnelT + "\n", nil

		case args[0] == "inspect":
			if d.owner == "" {
				return "", errors.New("docker inspect: No such object: stagd-relay")
			}

			out := d.owner + "\nPATH=/usr/bin\n"

			if d.token != "" {
				out += "STAGD_RELAY_TOKEN=" + d.token + "\n"
			}

			return out, nil

		case args[0] == "network" && args[1] == "inspect" && !d.network:
			return "", errors.New("docker network: network stagd-relay not found")

		case args[0] == "network" && args[1] == "create":
			d.network = true

		case args[0] == "network" && args[1] == "rm":
			d.network = false

		case args[0] == "rm" && slices.Contains(args, tunnelContainer) && !slices.Contains(args, relayContainer):
			d.tunnel = false

		case args[0] == "rm":
			d.owner = ""

		case args[0] == "run" && slices.Contains(args, tunnelContainer):
			d.tunnel = true
			d.tunnelT = strings.TrimPrefix(env[0], "TUNNEL_TOKEN=")

		case args[0] == "run":
			d.owner = args[slices.Index(args, "--label")+1][len(relayConnectionLabel)+1:]
			d.token = strings.TrimPrefix(env[0], "STAGD_RELAY_TOKEN=")
		}

		return "", nil
	}

	t.Cleanup(func() { docker = before })
}

// relayRun is which call ran the relay's container: its docker run, not cloudflared's; -1 without one.
func (d *fakeDocker) relayRun() int {
	for i, c := range d.calls {
		if c[0] == "run" && !slices.Contains(c, tunnelContainer) {
			return i
		}
	}

	return -1
}

// ran is the docker subcommands run, e.g. "network create".
func (d *fakeDocker) ran() []string {
	var out []string

	for _, c := range d.calls {
		if c[0] == "network" {
			out = append(out, c[0]+" "+c[1])
		} else {
			out = append(out, c[0])
		}
	}

	return out
}

// fakeDockerCompute is a Docker connection waiting for its relay, which
// connects on the second look once it's been given a new token.
type fakeDockerCompute struct {
	apiv1connect.UnimplementedComputeServiceHandler
	installed bool
	looks     int
	deleted   string
	tunnel    string // the tunnel token Stagd answers with; "" without tunnels
	connected bool   // its relay is connected already
}

var dockerConn = &apiv1.ComputeConnection{
	Id:       "01a17d0b-d7d0-7763-b435-c2fdfc9f3691",
	Provider: apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER,
	Name:     "docker-desktop",
	Status:   apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_FAILING,
}

const dockerToken = "stagd_relay.01a17a20-9557-737d-9f14-3ffd09591637.01a17d0b-d7d0-7763-b435-c2fdfc9f3691.secret"

func (f *fakeDockerCompute) ListProviders(context.Context, *connect.Request[apiv1.ListProvidersRequest]) (*connect.Response[apiv1.ListProvidersResponse], error) {
	return connect.NewResponse(&apiv1.ListProvidersResponse{Providers: []*apiv1.ProviderInfo{{Provider: apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER, Name: "Docker"}}}), nil
}

func (f *fakeDockerCompute) CreateComputeConnection(context.Context, *connect.Request[apiv1.CreateComputeConnectionRequest]) (*connect.Response[apiv1.CreateComputeConnectionResponse], error) {
	f.installed = true

	return connect.NewResponse(&apiv1.CreateComputeConnectionResponse{Connection: dockerConn, RelayToken: dockerToken, TunnelToken: f.tunnel}), nil
}

func (f *fakeDockerCompute) CreateRelayInstall(context.Context, *connect.Request[apiv1.CreateRelayInstallRequest]) (*connect.Response[apiv1.CreateRelayInstallResponse], error) {
	f.installed = true

	return connect.NewResponse(&apiv1.CreateRelayInstallResponse{RelayToken: dockerToken, TunnelToken: f.tunnel}), nil
}

func (f *fakeDockerCompute) ListComputeConnections(context.Context, *connect.Request[apiv1.ListComputeConnectionsRequest]) (*connect.Response[apiv1.ListComputeConnectionsResponse], error) {
	conn := dockerConn

	if f.installed || f.connected {
		if f.looks++; f.looks >= 2 || f.connected {
			conn = &apiv1.ComputeConnection{Id: dockerConn.Id, Provider: dockerConn.Provider, Name: dockerConn.Name, Account: "orbstack (Docker 29.8.2)", Status: apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_ACTIVE}
		}
	}

	return connect.NewResponse(&apiv1.ListComputeConnectionsResponse{Connections: []*apiv1.ComputeConnection{conn}}), nil
}

func (f *fakeDockerCompute) DeleteComputeConnection(_ context.Context, req *connect.Request[apiv1.DeleteComputeConnectionRequest]) (*connect.Response[apiv1.DeleteComputeConnectionResponse], error) {
	f.deleted = req.Msg.GetId()

	return connect.NewResponse(&apiv1.DeleteComputeConnectionResponse{}), nil
}

func dockerServer(t *testing.T, f *fakeDockerCompute) *httptest.Server {
	t.Helper()

	relayPoll = 10 * time.Millisecond
	t.Cleanup(func() { relayPoll = 2 * time.Second })

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewComputeServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

// A Stagd on this host's localhost is the host's to a relay in a container.
func TestRelayStagdURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:8792":         "http://host.docker.internal:8792",
		"http://127.0.0.1:8080/":        "http://host.docker.internal:8080/",
		"https://app.stagd.dev":         "https://app.stagd.dev",
		"https://app.stagd-staging.dev": "https://app.stagd-staging.dev",
	} {
		if got, _ := relayStagdURL(in); got != want {
			t.Errorf("relayStagdURL(%q) = %q, want %q", in, got, want)
		}
	}

	if args := strings.Join(dockerRunArgs("http://localhost:8792", "cc-1", "img", false), " "); !strings.Contains(args, "--add-host host.docker.internal:host-gateway") || !strings.Contains(args, "STAGD_URL=http://host.docker.internal:8792") {
		t.Errorf("on the host: %s", args)
	}

	if args := strings.Join(dockerRunArgs("https://app.stagd.dev", "cc-1", "img", false), " "); strings.Contains(args, "--add-host") {
		t.Errorf("hosted: %s", args)
	}
}

// install --run makes the network, starts the relay with the new token in
// docker's environment only, and waits for it; it prints no token.
func TestComputeInstallRunsTheDockerRelay(t *testing.T) {
	d := &fakeDocker{}
	d.install(t)

	srv := dockerServer(t, &fakeDockerCompute{})

	out, err := run(t, srv, "--token", "tok-1", "compute", "install", "docker-desktop", "--run")
	if err != nil {
		t.Fatalf("install --run: %v\n%s", err, out)
	}

	// Looked for a relay here to keep its token, found none: a new one, on a new network.
	if got := strings.Join(d.ran(), ", "); got != "inspect, inspect, network inspect, network create, pull, run, rm" {
		t.Errorf("ran %s", got)
	}

	at := d.relayRun()
	runArgs := strings.Join(d.calls[at], " ")

	for _, want := range []string{"--name stagd-relay", "--network stagd-relay", "--user root", "-e STAGD_RELAY_TOKEN ", "relay"} {
		if !strings.Contains(runArgs, want) {
			t.Errorf("no %q in docker %s", want, runArgs)
		}
	}

	if strings.Contains(runArgs, "secret") || !slices.Contains(d.envs[at], "STAGD_RELAY_TOKEN="+dockerToken) {
		t.Errorf("the token: in the arguments %q, or not in the environment %v", runArgs, d.envs[at])
	}

	if strings.Contains(out, "stagd_relay.") || !strings.Contains(out, "Connected: orbstack (Docker 29.8.2)") {
		t.Errorf("output:\n%s", out)
	}

	// Again, now it's connected: this connection's relay is replaced, on the network that's there, with --new-token.
	d.calls = nil

	if out, err := run(t, srv, "--token", "tok-1", "compute", "install", "docker-desktop", "--run", "--new-token", "--yes"); err != nil {
		t.Fatalf("again: %v\n%s", err, out)
	}

	if got := strings.Join(d.ran(), ", "); got != "inspect, rm, network inspect, pull, run, rm" {
		t.Errorf("again, ran %s", got)
	}
}

// Run again on the same host, it updates the relay there in place, keeping its token and cloudflared's: no
// question, no new token, so nothing is cut off.
func TestComputeInstallRunUpdatesKeepingTheToken(t *testing.T) {
	d := &fakeDocker{network: true, owner: dockerConn.Id, token: dockerToken, tunnel: true, tunnelT: "tunnel-secret"}
	d.install(t)

	f := &fakeDockerCompute{connected: true}
	srv := dockerServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "compute", "install", "docker-desktop", "--run")
	if err != nil {
		t.Fatalf("install --run: %v\n%s", err, out)
	}

	if f.installed {
		t.Error("asked Stagd for a new token")
	}

	if !strings.Contains(out, "keeps its token") || strings.Contains(out, "stagd_relay.") || strings.Contains(out, "tunnel-secret") {
		t.Errorf("output:\n%s", out)
	}

	var relayEnv, tunnelEnv []string

	for i, c := range d.calls {
		if c[0] == "run" && slices.Contains(c, tunnelContainer) {
			tunnelEnv = d.envs[i]
		} else if c[0] == "run" {
			relayEnv = d.envs[i]
		}
	}

	if !slices.Contains(relayEnv, "STAGD_RELAY_TOKEN="+dockerToken) || !slices.Contains(tunnelEnv, "TUNNEL_TOKEN=tunnel-secret") {
		t.Errorf("relay ran with %v, cloudflared with %v", relayEnv, tunnelEnv)
	}
}

// A relay of this connection's running elsewhere is cut off by a new token: asked, and no is no change, said.
func TestComputeInstallRunNoNewTokenSaysNothingChanged(t *testing.T) {
	d := &fakeDocker{network: true}
	d.install(t)

	f := &fakeDockerCompute{connected: true}
	srv := dockerServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "compute", "install", "docker-desktop", "--run"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no terminal, a connected relay elsewhere: want to be told about --yes, err = %v", err)
	}

	if slices.Contains(d.ran(), "run") || f.installed {
		t.Errorf("ran %v, new token %v", d.ran(), f.installed)
	}
}

// Another connection's relay on this host stays; --run is for Docker only.
func TestComputeInstallRunLeavesAnotherRelay(t *testing.T) {
	d := &fakeDocker{network: true, owner: "someone-else"}
	d.install(t)

	srv := dockerServer(t, &fakeDockerCompute{})

	if _, err := run(t, srv, "--token", "tok-1", "compute", "install", "docker-desktop", "--run"); err == nil || !strings.Contains(err.Error(), "another connection") {
		t.Fatalf("err = %v", err)
	}

	if slices.Contains(d.ran(), "rm") || slices.Contains(d.ran(), "run") {
		t.Errorf("ran %v", d.ran())
	}

	kube := relayedServer(t, &fakeRelayed{})

	if _, err := run(t, kube, "--token", "tok-1", "compute", "install", "cluster", "--run"); err == nil || !strings.Contains(err.Error(), "kubectl") {
		t.Errorf("kubernetes --run: err = %v", err)
	}
}

// connect --run makes the connection and runs its relay.
func TestComputeConnectRunsTheDockerRelay(t *testing.T) {
	d := &fakeDocker{}
	d.install(t)

	srv := dockerServer(t, &fakeDockerCompute{})

	out, err := run(t, srv, "--token", "tok-1", "compute", "connect", "docker", "--name", "docker-desktop", "--run")
	if err != nil {
		t.Fatalf("connect --run: %v\n%s", err, out)
	}

	if !slices.Contains(d.ran(), "run") || strings.Contains(out, "stagd_relay.") || !strings.Contains(out, "Connected:") {
		t.Errorf("ran %v:\n%s", d.ran(), out)
	}
}

// uninstall docker prints what removes the relay; --run removes it and its network.
func TestComputeUninstallDocker(t *testing.T) {
	d := &fakeDocker{network: true, owner: dockerConn.Id}
	d.install(t)

	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	if _, err := run(t, srv, "compute", "uninstall", "docker"); err == nil || !strings.Contains(err.Error(), "--run") {
		t.Fatalf("neither --run nor --print: err = %v", err)
	}

	out, err := run(t, srv, "compute", "uninstall", "docker", "--print")
	if err != nil || out != "  docker rm -f stagd-relay stagd-tunnel\n  docker network rm stagd-relay\n" || len(d.calls) != 0 {
		t.Fatalf("print: %v, ran %v:\n%s", err, d.ran(), out)
	}

	out, err = run(t, srv, "compute", "uninstall", "docker", "--run")
	if err != nil || d.owner != "" || d.network || !strings.Contains(out, "Removed the relay (stagd-relay) and its network") {
		t.Fatalf("--run: %v, ran %v:\n%s", err, d.ran(), out)
	}

	if out, err := run(t, srv, "compute", "uninstall", "docker", "--run"); err != nil || !strings.Contains(out, "No relay runs") {
		t.Errorf("nothing left: %v:\n%s", err, out)
	}
}

// Deleting a Docker connection whose relay runs here asks to remove it too.
func TestComputeDeleteAsksAboutTheRelayHere(t *testing.T) {
	before := interactive
	interactive = func() bool { return true }
	t.Cleanup(func() { interactive = before })

	for answer, removed := range map[string]bool{"y\ny\n": true, "y\nn\n": false} {
		d := &fakeDocker{network: true, owner: dockerConn.Id}
		d.install(t)

		f := &fakeDockerCompute{}
		srv := dockerServer(t, f)
		// One byte at a time, as a terminal gives each answer: each prompt reads only its own line.
		stdin = iotest.OneByteReader(strings.NewReader(answer))

		out, err := run(t, srv, "--token", "tok-1", "compute", "delete", "docker-desktop")
		if err != nil || f.deleted != dockerConn.Id {
			t.Fatalf("%q: %v, deleted %q:\n%s", answer, err, f.deleted, out)
		}

		if (d.owner == "") != removed {
			t.Errorf("%q: relay removed %v, ran %v", answer, d.owner == "", d.ran())
		}

		if !removed && !strings.Contains(out, "stagd compute uninstall docker --run") {
			t.Errorf("%q: no way to remove it later:\n%s", answer, out)
		}
	}

	stdin = nil
}

// Without --run, a Docker connection points at it, with no token; --print
// prints docker's commands with the token; install asks which first.
func TestComputeDockerPrint(t *testing.T) {
	d := &fakeDocker{}
	d.install(t)

	srv := dockerServer(t, &fakeDockerCompute{})

	out, err := run(t, srv, "--token", "tok-1", "compute", "connect", "docker", "--name", "docker-desktop")
	if err != nil || !strings.Contains(out, "stagd compute install docker-desktop --run") || strings.Contains(out, "stagd_relay.") || strings.Contains(out, "docker run") {
		t.Fatalf("connect: %v:\n%s", err, out)
	}

	f := &fakeDockerCompute{}
	srv = dockerServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "compute", "install", "docker-desktop"); err == nil || f.installed {
		t.Fatalf("install, neither: err = %v, new token %v", err, f.installed)
	}

	out, err = run(t, srv, "--token", "tok-1", "compute", "install", "docker-desktop", "--print")
	if err != nil || !strings.Contains(out, "docker network create stagd-relay") || !strings.Contains(out, "--network stagd-relay --user root") || !strings.Contains(out, dockerToken) || len(d.calls) != 0 {
		t.Fatalf("install --print: %v, ran %v:\n%s", err, d.ran(), out)
	}
}

// With a Cloudflare Tunnel, cloudflared runs beside the relay, on its network, its token in docker's
// environment only; the relay takes preview traffic from it. Uninstalling removes both.
func TestComputeInstallRunsCloudflared(t *testing.T) {
	d := &fakeDocker{}
	d.install(t)

	srv := dockerServer(t, &fakeDockerCompute{tunnel: "cf-tunnel-secret"})

	out, err := run(t, srv, "--token", "tok-1", "compute", "install", "docker-desktop", "--run")
	if err != nil || !d.tunnel || !strings.Contains(out, "Cloudflare Tunnel") || strings.Contains(out, "cf-tunnel-secret") {
		t.Fatalf("install --run: %v, cloudflared %v:\n%s", err, d.tunnel, out)
	}

	relayArgs := strings.Join(d.calls[d.relayRun()], " ")
	tunnelArgs := strings.Join(d.calls[len(d.calls)-1], " ")

	if !strings.Contains(relayArgs, "STAGD_RELAY_HTTP=:8081") {
		t.Errorf("the relay takes no tunnel traffic: %s", relayArgs)
	}

	if !strings.Contains(tunnelArgs, "--name stagd-tunnel") || !strings.Contains(tunnelArgs, "--network stagd-relay") || strings.Contains(tunnelArgs, "cf-tunnel-secret") || !slices.Contains(d.envs[len(d.envs)-1], "TUNNEL_TOKEN=cf-tunnel-secret") {
		t.Errorf("cloudflared: docker %s, environment %v", tunnelArgs, d.envs[len(d.envs)-1])
	}

	if _, err := run(t, srv, "compute", "uninstall", "docker", "--run"); err != nil || d.tunnel || d.owner != "" {
		t.Errorf("uninstall: %v, cloudflared %v, relay %q", err, d.tunnel, d.owner)
	}
}

// The relay's help shows the docker commands --print gives, for the CLI's own release.
func TestRelayHelpShowsDockerCommand(t *testing.T) {
	help := relayCommand("v1.2.3").Description

	want := dockerRunCommand(DefaultAPIURL, "<the relay token>", "ghcr.io/stagdhq/stagd:1.2.3", "")

	if !strings.Contains(help, want) {
		t.Fatalf("the help's docker commands aren't --print's:\n%s\nwant:\n%s", help, want)
	}

	for _, s := range []string{"--network stagd-relay --user root", "stagd-tunnel", "STAGD_RELAY_HTTP=:8081", "host.docker.internal:host-gateway"} {
		if !strings.Contains(help, s) {
			t.Errorf("the help doesn't mention %s", s)
		}
	}
}
