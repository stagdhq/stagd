package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "stagd.dev/stagd/internal/gen/stagd/api/v1"
	"stagd.dev/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeCompute offers the fake provider and records what it was sent.
type fakeCompute struct {
	apiv1connect.UnimplementedComputeServiceHandler
	created *apiv1.CreateComputeConnectionRequest
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

	if _, err := run(t, srv, "--token", "tok-1", "compute", "delete", "--yes", "fake"); err != nil || f.deleted != "cc-1" {
		t.Fatalf("delete: %v, deleted %q", err, f.deleted)
	}
}
