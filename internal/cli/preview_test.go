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

// fakePreviews streams `steps` on watch and records what it was asked.
type fakePreviews struct {
	apiv1connect.UnimplementedPreviewServiceHandler
	created *apiv1.CreatePreviewRequest
	steps   []apiv1.PreviewStatus
	failure string
}

func (f *fakePreviews) preview(s apiv1.PreviewStatus) *apiv1.Preview {
	p := &apiv1.Preview{
		Id:         "p-1",
		Repository: &apiv1.Repository{FullName: "acme-inc/web"},
		Change:     &apiv1.ChangeRequest{Number: 42},
		Sha:        "abc1234def",
		Status:     s,
		Url:        "https://pr-42-web-acme.stagd.run",
		Ingresses: []*apiv1.PreviewIngress{
			{Name: "web", Url: "https://pr-42-web-acme.stagd.run", Primary: true, Protected: true},
			{Name: "api", Url: "https://api--pr-42-web-acme.stagd.run"},
		},
	}

	if s == apiv1.PreviewStatus_PREVIEW_STATUS_FAILED {
		p.Error = f.failure
	}

	return p
}

func (f *fakePreviews) CreatePreview(_ context.Context, req *connect.Request[apiv1.CreatePreviewRequest]) (*connect.Response[apiv1.CreatePreviewResponse], error) {
	f.created = req.Msg

	return connect.NewResponse(&apiv1.CreatePreviewResponse{Preview: f.preview(apiv1.PreviewStatus_PREVIEW_STATUS_QUEUED)}), nil
}

func (f *fakePreviews) WatchPreview(_ context.Context, _ *connect.Request[apiv1.WatchPreviewRequest], stream *connect.ServerStream[apiv1.WatchPreviewResponse]) error {
	for _, s := range f.steps {
		if err := stream.Send(&apiv1.WatchPreviewResponse{Preview: f.preview(s), Message: statusLabel(s)}); err != nil {
			return err
		}
	}

	return nil
}

func previewServer(t *testing.T, f *fakePreviews) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestPreviewCreateFollowsUntilLive(t *testing.T) {
	f := &fakePreviews{steps: []apiv1.PreviewStatus{
		apiv1.PreviewStatus_PREVIEW_STATUS_QUEUED,
		apiv1.PreviewStatus_PREVIEW_STATUS_BUILDING,
		apiv1.PreviewStatus_PREVIEW_STATUS_LIVE,
	}}
	srv := previewServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "preview", "create", "--repo", "acme-inc/web", "--pr", "42")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}

	if f.created.GetRepository() != "acme-inc/web" || f.created.GetSource().GetChangeNumber() != 42 {
		t.Errorf("sent %v", f.created)
	}

	for _, want := range []string{"PR #42 @ abc1234", "building", "* web", "https://pr-42-web-acme.stagd.run", "api        https://api--pr-42-web-acme.stagd.run  (anyone with the link)", "(org members)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
}

func TestPreviewCreateFailsWhenThePreviewFails(t *testing.T) {
	srv := previewServer(t, &fakePreviews{
		steps:   []apiv1.PreviewStatus{apiv1.PreviewStatus_PREVIEW_STATUS_DEPLOYING, apiv1.PreviewStatus_PREVIEW_STATUS_FAILED},
		failure: "The workspace is out of capacity.",
	})

	_, err := run(t, srv, "--token", "tok-1", "preview", "create", "--repo", "acme-inc/web", "--branch", "main")
	if err == nil || !strings.Contains(err.Error(), "out of capacity") {
		t.Fatalf("err = %v", err)
	}
}

func TestPreviewCreateNeedsExactlyOneSource(t *testing.T) {
	srv := previewServer(t, &fakePreviews{})

	for _, args := range [][]string{{}, {"--pr", "1", "--branch", "main"}} {
		full := append([]string{"--token", "tok-1", "preview", "create", "--repo", "acme-inc/web"}, args...)
		if _, err := run(t, srv, full...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

// fakePolicy keeps one policy and records updates.
type fakePolicy struct {
	fakePreviews
	policy  *apiv1.PreviewPolicy
	updated *apiv1.UpdatePreviewPolicyRequest
}

func (f *fakePolicy) GetPreviewPolicy(_ context.Context, req *connect.Request[apiv1.GetPreviewPolicyRequest]) (*connect.Response[apiv1.GetPreviewPolicyResponse], error) {
	return connect.NewResponse(&apiv1.GetPreviewPolicyResponse{Policy: f.policy, Inherited: req.Msg.GetRepository() != ""}), nil
}

func (f *fakePolicy) UpdatePreviewPolicy(_ context.Context, req *connect.Request[apiv1.UpdatePreviewPolicyRequest]) (*connect.Response[apiv1.UpdatePreviewPolicyResponse], error) {
	f.updated = req.Msg
	f.policy = req.Msg.GetPolicy()

	return connect.NewResponse(&apiv1.UpdatePreviewPolicyResponse{Policy: f.policy}), nil
}

func TestPreviewPolicyChangesOnlyWhatsAsked(t *testing.T) {
	f := &fakePolicy{policy: &apiv1.PreviewPolicy{Access: apiv1.PreviewAccess_PREVIEW_ACCESS_ORG, PublicIngresses: []string{"api"}}}
	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	out, err := run(t, srv, "--token", "tok-1", "preview", "policy")
	if err != nil || !strings.Contains(out, "access:           org") || !strings.Contains(out, "public ingresses: api") {
		t.Fatalf("show: %v\n%s", err, out)
	}

	if _, err := run(t, srv, "--token", "tok-1", "preview", "policy", "--repo", "acme-inc/web", "--unguessable-urls"); err != nil {
		t.Fatal(err)
	}

	got := f.updated.GetPolicy()
	if got.GetRepository() != "acme-inc/web" || !got.GetUnguessableUrls() || got.GetAccess() != apiv1.PreviewAccess_PREVIEW_ACCESS_ORG || len(got.GetPublicIngresses()) != 1 {
		t.Errorf("sent %v", got)
	}

	if _, err := run(t, srv, "--token", "tok-1", "preview", "policy", "--access", "everyone"); err == nil {
		t.Error("--access everyone should be refused")
	}
}
