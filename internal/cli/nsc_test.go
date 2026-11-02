package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeNamespaceCompute offers Namespace, and can refuse a token as Stagd's check does.
type fakeNamespaceCompute struct {
	fakeCompute
	refuse bool
}

var namespaceConn = &apiv1.ComputeConnection{Id: "cc-ns", Provider: apiv1.ComputeProvider_COMPUTE_PROVIDER_NAMESPACE, Name: "namespace", Account: "Namespace workspace (eu, 2x4)"}

func (f *fakeNamespaceCompute) ListProviders(context.Context, *connect.Request[apiv1.ListProvidersRequest]) (*connect.Response[apiv1.ListProvidersResponse], error) {
	info := &apiv1.ProviderInfo{
		Provider: apiv1.ComputeProvider_COMPUTE_PROVIDER_NAMESPACE,
		Name:     "Namespace",
		Fields:   []*apiv1.ProviderField{{Key: "token", Label: "Workspace token", Secret: true, Required: true}, {Key: "region", Label: "Region", Choices: []string{"eu", "us"}}},
	}

	return connect.NewResponse(&apiv1.ListProvidersResponse{Providers: []*apiv1.ProviderInfo{info}}), nil
}

func (f *fakeNamespaceCompute) ListComputeConnections(context.Context, *connect.Request[apiv1.ListComputeConnectionsRequest]) (*connect.Response[apiv1.ListComputeConnectionsResponse], error) {
	return connect.NewResponse(&apiv1.ListComputeConnectionsResponse{Connections: []*apiv1.ComputeConnection{namespaceConn}}), nil
}

func (f *fakeNamespaceCompute) CreateComputeConnection(_ context.Context, req *connect.Request[apiv1.CreateComputeConnectionRequest]) (*connect.Response[apiv1.CreateComputeConnectionResponse], error) {
	f.created = req.Msg

	if f.refuse {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the workspace token can't keep a preview's instance"))
	}

	return connect.NewResponse(&apiv1.CreateComputeConnectionResponse{Connection: namespaceConn}), nil
}

func (f *fakeNamespaceCompute) UpdateComputeConnection(_ context.Context, req *connect.Request[apiv1.UpdateComputeConnectionRequest]) (*connect.Response[apiv1.UpdateComputeConnectionResponse], error) {
	f.updated = req.Msg

	if f.refuse {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Namespace refused the credentials"))
	}

	return connect.NewResponse(&apiv1.UpdateComputeConnectionResponse{Connection: namespaceConn}), nil
}

func namespaceServer(t *testing.T, f *fakeNamespaceCompute) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewComputeServiceHandler(f)

	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)

	t.Cleanup(srv.Close)

	return srv
}

// fakeNsc stands in for nsc: it makes tokens, lists them and revokes them, and remembers what it was asked.
type fakeNsc struct {
	tokens  []nscToken
	created []string
	revoked []string
}

func (n *fakeNsc) install(t *testing.T) {
	t.Helper()

	run, has := runNsc, hasNsc

	t.Cleanup(func() { runNsc, hasNsc = run, has })

	hasNsc = func() bool { return true }
	runNsc = func(_ context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "token create":
			n.created = append(n.created, strings.Join(args, " "))
			n.tokens = append(n.tokens, nscToken{ID: "tok_new", Name: args[3], State: "active"})

			return []byte("nsc_secret_value\n"), nil
		case "token list":
			return json.Marshal(n.tokens)
		case "token revoke":
			n.revoked = append(n.revoked, args[3])

			return nil, nil
		}

		return nil, errors.New("unexpected nsc " + strings.Join(args, " "))
	}
}

func TestConnectNamespaceMakesItsToken(t *testing.T) {
	f := &fakeNamespaceCompute{}
	n := &fakeNsc{}

	n.install(t)

	out, err := run(t, namespaceServer(t, f), "--token", "tok-1", "compute", "connect", "namespace", "--new-token", "--value", "region=eu")
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, out)
	}

	if got := f.created.GetValues()["token"]; got != "nsc_secret_value" {
		t.Fatalf("sent token %q", got)
	}

	// With every grant Stagd uses, for a year, named for the connection.
	made := n.created[len(n.created)-1]

	for _, want := range append([]string{"--name stagd-namespace-", "--expires_in 8760h", "--output token"}, namespaceGrants...) {
		if !strings.Contains(made, want) {
			t.Errorf("nsc token create without %s:\n%s", want, made)
		}
	}

	if strings.Contains(out, "nsc_secret_value") {
		t.Errorf("the token was shown:\n%s", out)
	}
}

func TestConnectNamespaceRevokesATokenStagdRefused(t *testing.T) {
	f := &fakeNamespaceCompute{refuse: true}
	n := &fakeNsc{}

	n.install(t)

	if _, err := run(t, namespaceServer(t, f), "--token", "tok-1", "compute", "connect", "namespace", "--new-token", "--value", "region=eu"); err == nil {
		t.Fatal("connected with a refused token")
	}

	if strings.Join(n.revoked, ",") != "tok_new" {
		t.Fatalf("revoked %v", n.revoked)
	}
}

func TestUpdateNamespaceReplacesItsToken(t *testing.T) {
	f := &fakeNamespaceCompute{}
	n := &fakeNsc{tokens: []nscToken{
		{ID: "tok_old", Name: "stagd-namespace-20251101-090000", State: "active"},
		{ID: "tok_other", Name: "stagd-staging-20251101-090000", State: "active"},
		{ID: "tok_mine", Name: "stagd token", State: "active"},
	}}

	n.install(t)

	out, err := run(t, namespaceServer(t, f), "--token", "tok-1", "compute", "update", "namespace", "--new-token")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}

	if f.updated.GetValues()["token"] != "nsc_secret_value" {
		t.Fatalf("sent %v", f.updated.GetValues())
	}

	// Only this connection's tokens the CLI made: not another connection's, not one made by hand.
	if strings.Join(n.revoked, ",") != "tok_old" {
		t.Fatalf("revoked %v", n.revoked)
	}

	if !strings.Contains(out, "Revoked the old ones: stagd-namespace-20251101-090000.") {
		t.Errorf("output:\n%s", out)
	}
}
