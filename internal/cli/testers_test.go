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

// fakeTesters is a PreviewService with two testers: admin signs in, api doesn't.
type fakeTesters struct {
	apiv1connect.UnimplementedPreviewServiceHandler
	checked []string
}

func (f *fakeTesters) ListTesters(_ context.Context, _ *connect.Request[apiv1.ListTestersRequest]) (*connect.Response[apiv1.ListTestersResponse], error) {
	return connect.NewResponse(&apiv1.ListTestersResponse{Testers: []*apiv1.PreviewTester{
		{Name: "admin", Email: "admin@acme.test", SignIn: "email-code", How: "/login, Email: admin@acme.test, Code: 123456", Note: "org admin"},
		{Name: "api", SignIn: "token", How: "Authorization: Bearer the token", Headers: true},
	}}), nil
}

func (f *fakeTesters) CheckTesters(_ context.Context, req *connect.Request[apiv1.CheckTestersRequest]) (*connect.Response[apiv1.CheckTestersResponse], error) {
	f.checked = req.Msg.GetNames()

	return connect.NewResponse(&apiv1.CheckTestersResponse{Checks: []*apiv1.TesterCheck{
		{Name: "admin", Ok: true, Steps: []*apiv1.TesterStep{{Text: "open /login", Ok: true}, {Text: "signed in: Sign out shows", Ok: true}}},
		{Name: "api", Error: "step 1, signed in: GET /api/me answers 2xx: answered 401", Steps: []*apiv1.TesterStep{{Text: "signed in: GET /api/me answers 2xx", Error: "answered 401"}}, Screenshot: []byte{1, 2}},
	}}), nil
}

func TestPreviewTesters(t *testing.T) {
	f := &fakeTesters{}
	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	out, err := run(t, srv, "--token", "tok-1", "preview", "testers", "p-1")
	if err != nil || !strings.Contains(out, "/login, Email: admin@acme.test, Code: 123456") || !strings.Contains(out, "Authorization: Bearer the token") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	dir := t.TempDir()

	out, err = run(t, srv, "--token", "tok-1", "preview", "testers", "p-1", "--check", "--screenshots", dir, "admin", "api")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 sign-ins didn't work") {
		t.Fatalf("check should fail for api: %v\n%s", err, out)
	}

	if strings.Join(f.checked, ",") != "admin,api" || !strings.Contains(out, "✓ admin") || !strings.Contains(out, "✗ api") || !strings.Contains(out, "answered 401") {
		t.Fatalf("check: %v\n%s", f.checked, out)
	}

	if b, err := os.ReadFile(filepath.Join(dir, "api.jpg")); err != nil || len(b) != 2 {
		t.Fatalf("screenshot: %v %v", err, b)
	}
}
