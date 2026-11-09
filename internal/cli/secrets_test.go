package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeSecrets reveals SESSION_SECRET in acme/web, and nothing else.
type fakeSecrets struct {
	apiv1connect.UnimplementedSecretServiceHandler
}

func (fakeSecrets) RevealSecret(_ context.Context, req *connect.Request[apiv1.RevealSecretRequest]) (*connect.Response[apiv1.RevealSecretResponse], error) {
	if req.Msg.GetRepository() != "acme/web" || req.Msg.GetName() != "SESSION_SECRET" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	return connect.NewResponse(&apiv1.RevealSecretResponse{Value: "s3cret-value"}), nil
}

func TestSecretsGetPrintsTheValueAlone(t *testing.T) {
	mux := http.NewServeMux()

	path, handler := apiv1connect.NewSecretServiceHandler(fakeSecrets{})
	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	out, err := run(t, srv, "--token", "tok-1", "secrets", "get", "--repo", "acme/web", "SESSION_SECRET")
	if err != nil {
		t.Fatal(err)
	}

	if out != "s3cret-value\n" {
		t.Errorf("got %q", out)
	}

	if _, err := run(t, srv, "--token", "tok-1", "secrets", "get", "OTHER"); err == nil {
		t.Error("an unknown secret: want an error")
	}

	if _, err := run(t, srv, "--token", "tok-1", "secrets", "get"); err == nil {
		t.Error("no name: want an error")
	}
}
