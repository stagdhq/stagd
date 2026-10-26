package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeValidator asks once for deploy/compose.yaml, like the server does when
// stagd.yaml points at it, and records every request.
type fakeValidator struct {
	fakePreviews
	requests []*apiv1.ValidateAppRequest
}

func (f *fakeValidator) ValidateApp(_ context.Context, req *connect.Request[apiv1.ValidateAppRequest]) (*connect.Response[apiv1.ValidateAppResponse], error) {
	f.requests = append(f.requests, req.Msg)

	if _, ok := req.Msg.GetFiles()["deploy/compose.yaml"]; !ok && !contains(req.Msg.GetAbsent(), "deploy/compose.yaml") {
		return connect.NewResponse(&apiv1.ValidateAppResponse{Requested: []string{"deploy/compose.yaml"}}), nil
	}

	if strings.Contains(req.Msg.GetFiles()["deploy/compose.yaml"], "broken") {
		return connect.NewResponse(&apiv1.ValidateAppResponse{Files: []string{"stagd.yaml", "deploy/compose.yaml"}, Problems: []string{"deploy/compose.yaml: no services"}}), nil
	}

	return connect.NewResponse(&apiv1.ValidateAppResponse{
		Files:      []string{"stagd.yaml", "deploy/compose.yaml"},
		Components: []*apiv1.AppComponent{{Name: "web", BuildContext: "."}, {Name: "db", Image: "postgres:17"}},
		Ingresses:  []*apiv1.PreviewIngress{{Name: "web", Component: "web", Port: 3000, Primary: true}},
		Notes:      []string{"web: bind mount ./src left out"},
	}), nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}

	return false
}

func validateServer(t *testing.T, f *fakeValidator) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	for path, text := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestValidateSendsRootFilesThenWhatTheServerAsksFor(t *testing.T) {
	f := &fakeValidator{}
	srv := validateServer(t, f)
	dir := repo(t, map[string]string{"stagd.yaml": "app: { compose: deploy/compose.yaml }", "deploy/compose.yaml": "services: {}"})

	out, err := run(t, srv, "--token", "tok-1", "validate", dir)
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}

	if len(f.requests) != 2 {
		t.Fatalf("made %d requests, want 2", len(f.requests))
	}

	first := f.requests[0]
	if _, ok := first.GetFiles()["stagd.yaml"]; !ok || !contains(first.GetAbsent(), "compose.yaml") {
		t.Errorf("first request: files %v, absent %v", first.GetFiles(), first.GetAbsent())
	}

	for _, want := range []string{"Read stagd.yaml and deploy/compose.yaml", "built from .", "image postgres:17", "* web", "web:3000", "bind mount"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
}

func TestValidateFailsWithProblems(t *testing.T) {
	srv := validateServer(t, &fakeValidator{})
	dir := repo(t, map[string]string{"deploy/compose.yaml": "broken"})

	out, err := run(t, srv, "--token", "tok-1", "validate", dir)
	if err == nil || !strings.Contains(out, "✗ deploy/compose.yaml: no services") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestValidateRefusesPathsOutsideTheRepository(t *testing.T) {
	if _, _, err := readRepoFile(t.TempDir(), "../secret.yaml"); err == nil {
		t.Error("../secret.yaml should be refused")
	}

	if _, _, err := readRepoFile(t.TempDir(), "/etc/passwd"); err == nil {
		t.Error("/etc/passwd should be refused")
	}
}
