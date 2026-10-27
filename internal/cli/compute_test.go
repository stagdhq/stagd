package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeCompute offers the fake provider and records what it was sent.
type fakeCompute struct {
	apiv1connect.UnimplementedComputeServiceHandler
	created *apiv1.CreateComputeConnectionRequest
	updated *apiv1.UpdateComputeConnectionRequest
	deleted string
}

var fakeInfo = &apiv1.ProviderInfo{
	Provider:    apiv1.ComputeProvider_COMPUTE_PROVIDER_FAKE,
	Name:        "Fake",
	Description: "Runs nothing.",
	Fields: []*apiv1.ProviderField{
		{Key: "token", Label: "Token", Secret: true, Required: true},
		{Key: "region", Label: "Region", Choices: []string{"eu", "us"}},
	},
	Capabilities: &apiv1.ComputeCapabilities{Sleep: true, Regions: []string{"eu", "us"}},
}

var fakeConn = &apiv1.ComputeConnection{
	Id:       "cc-1",
	Provider: apiv1.ComputeProvider_COMPUTE_PROVIDER_FAKE,
	Name:     "fake",
	Account:  "fake-workspace (eu)",
	Status:   apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_ACTIVE,
	Default:  true,
}

func (f *fakeCompute) ListProviders(context.Context, *connect.Request[apiv1.ListProvidersRequest]) (*connect.Response[apiv1.ListProvidersResponse], error) {
	return connect.NewResponse(&apiv1.ListProvidersResponse{Providers: []*apiv1.ProviderInfo{fakeInfo}}), nil
}

func (f *fakeCompute) CreateComputeConnection(_ context.Context, req *connect.Request[apiv1.CreateComputeConnectionRequest]) (*connect.Response[apiv1.CreateComputeConnectionResponse], error) {
	f.created = req.Msg

	return connect.NewResponse(&apiv1.CreateComputeConnectionResponse{Connection: fakeConn}), nil
}

func (f *fakeCompute) ListComputeConnections(context.Context, *connect.Request[apiv1.ListComputeConnectionsRequest]) (*connect.Response[apiv1.ListComputeConnectionsResponse], error) {
	return connect.NewResponse(&apiv1.ListComputeConnectionsResponse{Connections: []*apiv1.ComputeConnection{fakeConn}}), nil
}

func (f *fakeCompute) UpdateComputeConnection(_ context.Context, req *connect.Request[apiv1.UpdateComputeConnectionRequest]) (*connect.Response[apiv1.UpdateComputeConnectionResponse], error) {
	f.updated = req.Msg

	return connect.NewResponse(&apiv1.UpdateComputeConnectionResponse{Connection: fakeConn}), nil
}

func (f *fakeCompute) DeleteComputeConnection(_ context.Context, req *connect.Request[apiv1.DeleteComputeConnectionRequest]) (*connect.Response[apiv1.DeleteComputeConnectionResponse], error) {
	f.deleted = req.Msg.GetId()

	return connect.NewResponse(&apiv1.DeleteComputeConnectionResponse{}), nil
}

func computeServer(t *testing.T, f *fakeCompute) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := apiv1connect.NewComputeServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestComputeConnectReadsSecretFromStdin(t *testing.T) {
	f := &fakeCompute{}
	srv := computeServer(t, f)
	stdin = strings.NewReader("s3cret\n")
	t.Cleanup(func() { stdin = nil })

	out, err := run(t, srv, "--token", "tok-1", "compute", "connect", "fake", "--value", "token=@-", "--value", "region=eu")
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, out)
	}

	if got := f.created.GetValues(); got["token"] != "s3cret" || got["region"] != "eu" {
		t.Errorf("sent values %v", got)
	}

	if f.created.GetName() != "fake" || f.created.GetProvider() != apiv1.ComputeProvider_COMPUTE_PROVIDER_FAKE {
		t.Errorf("sent %v", f.created)
	}

	if !strings.Contains(out, "New previews run on it") {
		t.Errorf("output:\n%s", out)
	}
}

func TestComputeConnectWithoutRequiredValueFailsInScripts(t *testing.T) {
	srv := computeServer(t, &fakeCompute{})

	_, err := run(t, srv, "--token", "tok-1", "compute", "connect", "fake", "--value", "region=eu")
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v", err)
	}

	if _, err := run(t, srv, "--token", "tok-1", "compute", "connect", "aws"); err == nil || !strings.Contains(err.Error(), "doesn't offer") {
		t.Fatalf("unknown provider: err = %v", err)
	}
}

func TestComputeListAndDeleteByName(t *testing.T) {
	f := &fakeCompute{}
	srv := computeServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "compute", "list")
	if err != nil || !strings.Contains(out, "fake-workspace (eu)") || !strings.Contains(out, "active") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "compute", "providers")
	if err != nil || !strings.Contains(out, "Runs nothing.") {
		t.Fatalf("providers: %v\n%s", err, out)
	}

	if out, err := run(t, srv, "--token", "tok-1", "compute", "delete", "--yes", "fake"); err != nil || f.deleted != "cc-1" || out != "Deleted fake.\n" {
		t.Fatalf("delete: %v, deleted %q\n%s", err, f.deleted, out)
	}
}

// After deleting, what's left on the compute and how to remove it: in a
// cluster, Stagd's install, cluster-wide rights too; on a Docker host, the relay.
func TestComputeCleanup(t *testing.T) {
	kube := cleanup(apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES)

	for _, want := range []string{"stagd compute uninstall kubernetes | kubectl delete --ignore-not-found -f -"} {
		if !strings.Contains(kube, want) {
			t.Errorf("kubernetes: no %q in\n%s", want, kube)
		}
	}

	if docker := cleanup(apiv1.ComputeProvider_COMPUTE_PROVIDER_DOCKER); !strings.Contains(docker, "stagd compute uninstall docker --run") {
		t.Errorf("docker: %s", docker)
	}

	if cleanup(apiv1.ComputeProvider_COMPUTE_PROVIDER_NAMESPACE) != "" {
		t.Error("namespace: Stagd's instances go with their previews")
	}
}

func TestComputeUpdateSendsOnlyWhatChanged(t *testing.T) {
	f := &fakeCompute{}
	srv := computeServer(t, f)
	stdin = strings.NewReader("new-token\n")
	t.Cleanup(func() { stdin = nil })

	out, err := run(t, srv, "--token", "tok-1", "compute", "update", "fake", "--value", "token=@-")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}

	if f.updated.GetId() != "cc-1" || len(f.updated.GetValues()) != 1 || f.updated.GetValues()["token"] != "new-token" {
		t.Errorf("sent %v", f.updated)
	}

	if !strings.Contains(out, "Updated fake") {
		t.Errorf("output:\n%s", out)
	}

	if _, err := run(t, srv, "--token", "tok-1", "compute", "update", "fake"); err == nil {
		t.Error("update without values: no error")
	}
}

// fakeRelayed is a Kubernetes connection through the relay, whose install
// manifest has a new token, and whose relay then connects.
type fakeRelayed struct {
	fakeCompute
	installed string
	looks     int
}

var relayConn = &apiv1.ComputeConnection{
	Id:            "cc-k8s",
	Provider:      apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES,
	Name:          "cluster",
	Status:        apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_FAILING,
	StatusMessage: "waiting for the relay to connect",
}

func (f *fakeRelayed) ListProviders(context.Context, *connect.Request[apiv1.ListProvidersRequest]) (*connect.Response[apiv1.ListProvidersResponse], error) {
	return connect.NewResponse(&apiv1.ListProvidersResponse{Providers: []*apiv1.ProviderInfo{{Provider: apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES, Name: "Kubernetes"}}}), nil
}

func (f *fakeRelayed) CreateComputeConnection(_ context.Context, req *connect.Request[apiv1.CreateComputeConnectionRequest]) (*connect.Response[apiv1.CreateComputeConnectionResponse], error) {
	f.created = req.Msg

	return connect.NewResponse(&apiv1.CreateComputeConnectionResponse{Connection: relayConn, RelayToken: "stagd_relay.secret"}), nil
}

const fakeManifest = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: stagd-relay\nstringData:\n  token: \"stagd_relay.new\"\n"

func (f *fakeRelayed) CreateInstallManifest(_ context.Context, req *connect.Request[apiv1.CreateInstallManifestRequest]) (*connect.Response[apiv1.CreateInstallManifestResponse], error) {
	f.installed = req.Msg.GetId()

	return connect.NewResponse(&apiv1.CreateInstallManifestResponse{Manifest: fakeManifest}), nil
}

// The relay connects on the third look.
func (f *fakeRelayed) ListComputeConnections(context.Context, *connect.Request[apiv1.ListComputeConnectionsRequest]) (*connect.Response[apiv1.ListComputeConnectionsResponse], error) {
	f.looks++

	conn := relayConn
	if f.looks >= 3 {
		conn = &apiv1.ComputeConnection{Id: relayConn.Id, Provider: relayConn.Provider, Name: relayConn.Name, Account: "v1.31.2 · 3 nodes", Status: apiv1.ComputeConnectionStatus_COMPUTE_CONNECTION_STATUS_ACTIVE}
	}

	return connect.NewResponse(&apiv1.ListComputeConnectionsResponse{Connections: []*apiv1.ComputeConnection{conn}}), nil
}

func relayedServer(t *testing.T, f *fakeRelayed) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := apiv1connect.NewComputeServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

// Connecting Kubernetes says to pipe its install to kubectl; it never
// prints the token.
func TestComputeConnectKubernetesPrintsTheInstall(t *testing.T) {
	f := &fakeRelayed{}
	srv := relayedServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "compute", "connect", "kubernetes", "--name", "cluster")
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, out)
	}

	if !strings.Contains(out, "stagd compute install cluster | kubectl apply -f -") || strings.Contains(out, "stagd_relay.secret") {
		t.Errorf("want the install command, not the token:\n%s", out)
	}
}

// install prints the cluster's manifest and nothing else, for kubectl; a
// waiting connection needs no confirmation; --wait waits on stderr, after
// stdout ends.
func TestComputeInstallPrintsTheManifest(t *testing.T) {
	relayPoll = 10 * time.Millisecond
	t.Cleanup(func() { relayPoll = 2 * time.Second })

	f := &fakeRelayed{}
	srv := relayedServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "compute", "install", "cluster")
	if err != nil || f.installed != "cc-k8s" || out != fakeManifest {
		t.Fatalf("install: %v, installed %q:\n%s", err, f.installed, out)
	}

	if out, err := run(t, srv, "--token", "tok-1", "compute", "install", "cluster", "--wait"); err != nil || out != fakeManifest || f.looks < 3 {
		t.Fatalf("install --wait: %v, looked %d times:\n%s", err, f.looks, out)
	}
}

// uninstall prints every manifest an install can bring, without tokens,
// for kubectl delete: Stagd's access and rights included.
func TestComputeUninstall(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	// The target is named, for others to come.
	if _, err := run(t, srv, "compute", "uninstall"); err == nil {
		t.Error("no target: it printed anyway")
	}

	if _, err := run(t, srv, "compute", "uninstall", "namespace"); err == nil {
		t.Error("namespace: nothing to uninstall, yet no error")
	}

	out, err := run(t, srv, "compute", "uninstall", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"kind: ValidatingAdmissionPolicy", "name: stagd-namespaces", "kind: ClusterRole", "name: stagd-system", "name: stagd-relay", "name: stagd-tunnel", "name: stagd-buildkitd", "\n---\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q", want)
		}
	}

	if strings.Contains(out, "stringData") || strings.Contains(out, "stagd_relay.") {
		t.Errorf("a token in the uninstall:\n%s", out)
	}
}

// The Docker relay's command names this CLI's release, or the latest one.
func TestRelayImage(t *testing.T) {
	for version, want := range map[string]string{"v0.1.0": "ghcr.io/stagdhq/stagd:0.1.0", "0.2.3": "ghcr.io/stagdhq/stagd:0.2.3", "dev": "ghcr.io/stagdhq/stagd:latest", "v0.1.0-5-gabc": "ghcr.io/stagdhq/stagd:latest"} {
		if got := relayImage(version); got != want {
			t.Errorf("relayImage(%q) = %q, want %q", version, got, want)
		}
	}
}
