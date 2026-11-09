package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeSecrets has one secret of each visibility, and records what it's asked to do.
type fakeSecrets struct {
	apiv1connect.UnimplementedSecretServiceHandler

	set     *apiv1.SetSecretsRequest
	updated *apiv1.UpdateSecretVisibilityRequest
}

func (f *fakeSecrets) ListSecrets(context.Context, *connect.Request[apiv1.ListSecretsRequest]) (*connect.Response[apiv1.ListSecretsResponse], error) {
	return connect.NewResponse(&apiv1.ListSecretsResponse{Secrets: []*apiv1.Secret{
		{Name: "API_URL", Visibility: apiv1.SecretVisibility_SECRET_VISIBILITY_VISIBLE, Value: "https://api.example.com"},
		{Name: "SESSION_SECRET", Visibility: apiv1.SecretVisibility_SECRET_VISIBILITY_HIDDEN},
		{Name: "STRIPE_KEY", Visibility: apiv1.SecretVisibility_SECRET_VISIBILITY_WRITE_ONLY},
	}}), nil
}

func (f *fakeSecrets) SetSecrets(_ context.Context, req *connect.Request[apiv1.SetSecretsRequest]) (*connect.Response[apiv1.SetSecretsResponse], error) {
	f.set = req.Msg

	return connect.NewResponse(&apiv1.SetSecretsResponse{}), nil
}

func (f *fakeSecrets) RevealSecret(_ context.Context, req *connect.Request[apiv1.RevealSecretRequest]) (*connect.Response[apiv1.RevealSecretResponse], error) {
	if req.Msg.GetName() == "STRIPE_KEY" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, nil)
	}

	return connect.NewResponse(&apiv1.RevealSecretResponse{Value: "s3cret-value"}), nil
}

func (f *fakeSecrets) UpdateSecretVisibility(_ context.Context, req *connect.Request[apiv1.UpdateSecretVisibilityRequest]) (*connect.Response[apiv1.UpdateSecretVisibilityResponse], error) {
	f.updated = req.Msg

	return connect.NewResponse(&apiv1.UpdateSecretVisibilityResponse{Secret: &apiv1.Secret{Name: req.Msg.GetName(), Visibility: req.Msg.GetVisibility()}}), nil
}

func secretsServer(t *testing.T, f *fakeSecrets) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	path, handler := apiv1connect.NewSecretServiceHandler(f)
	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestSecretsListShowsVisibleValuesOnly(t *testing.T) {
	srv := secretsServer(t, &fakeSecrets{})

	out, err := run(t, srv, "--token", "tok-1", "secrets", "list")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"https://api.example.com", "visible", "hidden", "write-only"} {
		if !strings.Contains(out, want) {
			t.Errorf("list doesn't say %q:\n%s", want, out)
		}
	}

	if strings.Count(out, "••••••••") != 2 {
		t.Errorf("want the hidden and write-only values masked:\n%s", out)
	}
}

func TestSecretsSetSendsItsVisibility(t *testing.T) {
	f := &fakeSecrets{}
	srv := secretsServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "secrets", "set", "--visibility", "visible", "API_URL=https://api.example.com"); err != nil {
		t.Fatal(err)
	}

	if f.set.GetVisibility() != apiv1.SecretVisibility_SECRET_VISIBILITY_VISIBLE {
		t.Errorf("visibility: got %v", f.set.GetVisibility())
	}

	// Without --visibility, the server decides: a new secret is write-only, a replaced one keeps its own.
	if _, err := run(t, srv, "--token", "tok-1", "secrets", "set", "API_URL=https://api.example.com"); err != nil {
		t.Fatal(err)
	}

	if f.set.GetVisibility() != apiv1.SecretVisibility_SECRET_VISIBILITY_UNSPECIFIED {
		t.Errorf("no --visibility: got %v", f.set.GetVisibility())
	}

	if _, err := run(t, srv, "--token", "tok-1", "secrets", "set", "--visibility", "masked", "A=b"); err == nil {
		t.Error("an unknown visibility: want an error")
	}
}

func TestSecretsGetPrintsTheValueAlone(t *testing.T) {
	srv := secretsServer(t, &fakeSecrets{})

	out, err := run(t, srv, "--token", "tok-1", "secrets", "get", "SESSION_SECRET")
	if err != nil {
		t.Fatal(err)
	}

	if out != "s3cret-value\n" {
		t.Errorf("got %q", out)
	}

	if _, err := run(t, srv, "--token", "tok-1", "secrets", "get", "STRIPE_KEY"); err == nil {
		t.Error("a write-only secret: want an error")
	}
}

func TestSecretsVisibility(t *testing.T) {
	f := &fakeSecrets{}
	srv := secretsServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "secrets", "visibility", "--repo", "acme/web", "SESSION_SECRET", "write-only")
	if err != nil {
		t.Fatal(err)
	}

	if f.updated.GetRepository() != "acme/web" || f.updated.GetVisibility() != apiv1.SecretVisibility_SECRET_VISIBILITY_WRITE_ONLY {
		t.Errorf("got %v", f.updated)
	}

	if !strings.Contains(out, "SESSION_SECRET in acme/web is write-only") {
		t.Errorf("got %q", out)
	}
}
