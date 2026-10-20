package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "stagd.dev/stagd/internal/gen/stagd/api/v1"
	"stagd.dev/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeAuth answers GetMe for the token "tok-1".
type fakeAuth struct {
	apiv1connect.UnimplementedAuthServiceHandler
	active *apiv1.Membership
}

func (f *fakeAuth) GetMe(_ context.Context, req *connect.Request[apiv1.GetMeRequest]) (*connect.Response[apiv1.GetMeResponse], error) {
	if req.Header().Get("Authorization") != "Bearer tok-1" {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	return connect.NewResponse(&apiv1.GetMeResponse{
		User:   &apiv1.User{Id: "u-1", Email: "kari@acme.test", Name: "Kari Nordmann"},
		Active: f.active,
	}), nil
}

// fakeServer is the api Worker's device-login endpoints plus AuthService.
// The first token poll is pending; the second returns the token.
func fakeServer(t *testing.T, auth *fakeAuth) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	polls := 0

	mux.HandleFunc("POST /auth/device/code", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body["client_id"] != "stagd-cli" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "dev-1",
			"user_code":                 "ABCD1234",
			"verification_uri":          "http://x/device",
			"verification_uri_complete": "http://x/device?user_code=ABCD1234",
			"expires_in":                60,
			"interval":                  0,
		})
	})

	mux.HandleFunc("POST /auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		polls++

		if polls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
	})

	path, handler := apiv1connect.NewAuthServiceHandler(auth)
	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func run(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := New("test")
	cmd.Writer = &out
	cmd.ErrWriter = &bytes.Buffer{}
	// Return exit errors to the test instead of exiting the process.
	cmd.ExitErrHandler = func(context.Context, *cli.Command, error) {}
	full := append([]string{"stagd", "--api-url", srv.URL}, args...)
	err := cmd.Run(context.Background(), full)

	return out.String(), err
}

func TestLoginStoresTokenAndWhoamiUsesIt(t *testing.T) {
	srv := fakeServer(t, &fakeAuth{})

	out, err := run(t, srv, "login", "--no-browser")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}

	for _, want := range []string{"ABCD1234", "Signed in as kari@acme.test (no org yet)", "stagd orgs create"} {
		if !strings.Contains(out, want) {
			t.Errorf("login output misses %q:\n%s", want, out)
		}
	}

	if got, err := store.Load(srv.URL); err != nil || got != "tok-1" {
		t.Fatalf("stored token = %q, %v", got, err)
	}

	out, err = run(t, srv, "whoami")
	if err != nil || !strings.Contains(out, "kari@acme.test") {
		t.Fatalf("whoami: %v\n%s", err, out)
	}
}

func TestWhoamiShowsActiveOrg(t *testing.T) {
	active := &apiv1.Membership{Org: &apiv1.Org{Id: "o-1", Slug: "acme", Name: "Acme"}, Role: "owner"}
	srv := fakeServer(t, &fakeAuth{active: active})

	out, err := run(t, srv, "--token", "tok-1", "whoami")
	if err != nil || !strings.Contains(out, "org: Acme · acme (owner)") {
		t.Fatalf("whoami: %v\n%s", err, out)
	}
}

func TestWhoamiWithoutSignInFails(t *testing.T) {
	srv := fakeServer(t, &fakeAuth{})
	_ = store.Delete(srv.URL)

	if _, err := run(t, srv, "whoami"); err == nil || !strings.Contains(err.Error(), "stagd login") {
		t.Fatalf("err = %v", err)
	}
}

func TestSlugFrom(t *testing.T) {
	for name, want := range map[string]string{"Acme Inc": "acme-inc", "  Ølfabrikk 2  ": "lfabrikk-2", "a--b": "a-b"} {
		if got := slugFrom(name); got != want {
			t.Errorf("slugFrom(%q) = %q, want %q", name, got, want)
		}
	}
}
