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

const starterYAML = "version: 1\n\ningress:\n  web: { service: web, port: 3000, primary: true }\n"

// fakeStarter offers a stagd.yaml for a checkout without one, as Stagd does.
type fakeStarter struct {
	fakePreviews

	sent    map[string]string
	absent  []string
	problem string
}

func (f *fakeStarter) ValidateApp(_ context.Context, req *connect.Request[apiv1.ValidateAppRequest]) (*connect.Response[apiv1.ValidateAppResponse], error) {
	f.sent = req.Msg.GetFiles()
	f.absent = req.Msg.GetAbsent()

	res := &apiv1.ValidateAppResponse{Files: []string{"compose.yaml"}, Starter: starterYAML}

	if f.problem != "" {
		res.Problems = []string{f.problem}
	}

	return connect.NewResponse(res), nil
}

func TestInitWritesTheStarterFromTheComposeFile(t *testing.T) {
	f := &fakeStarter{}
	dir := repo(t, map[string]string{"compose.yaml": "services:\n  web:\n    build: .\n"})

	out, err := runWith(t, f, "--token", "tok-1", "init", dir)
	if err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(dir, "stagd.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if string(written) != starterYAML {
		t.Errorf("stagd.yaml:\n%s", written)
	}

	if !strings.Contains(f.sent["compose.yaml"], "build: .") || !contains(f.absent, "stagd.yaml") {
		t.Errorf("sent %v, absent %v", f.sent, f.absent)
	}

	if !strings.Contains(out, "Wrote ") || !strings.Contains(out, "stagd validate") {
		t.Errorf("output:\n%s", out)
	}
}

func TestInitSaysWhatStopsAPreview(t *testing.T) {
	dir := repo(t, map[string]string{"compose.yaml": "services: {}\n"})

	out, err := runWith(t, &fakeStarter{problem: "compose.yaml: no services"}, "--token", "tok-1", "init", dir)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "Before previews start:\n  - compose.yaml: no services") {
		t.Errorf("output:\n%s", out)
	}
}

func TestInitLeavesAStagdYAMLThatsThere(t *testing.T) {
	dir := repo(t, map[string]string{"stagd.yaml": "version: 1\n", "compose.yaml": "services: {}\n"})

	_, err := runWith(t, &fakeStarter{}, "--token", "tok-1", "init", dir)

	if err == nil || !strings.Contains(err.Error(), "stagd.yaml is there already") {
		t.Errorf("err: %v", err)
	}

	if b, _ := os.ReadFile(filepath.Join(dir, "stagd.yaml")); string(b) != "version: 1\n" {
		t.Errorf("stagd.yaml changed: %q", b)
	}
}

// runWith runs the CLI against a fakeStarter.
func runWith(t *testing.T, f *fakeStarter, args ...string) (string, error) {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)

	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)

	t.Cleanup(srv.Close)

	return run(t, srv, args...)
}
