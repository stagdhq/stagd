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

// fakePreviews streams `steps` on watch and records what it was asked.
type fakePreviews struct {
	apiv1connect.UnimplementedPreviewServiceHandler
	created *apiv1.CreatePreviewRequest
	steps   []apiv1.PreviewStatus
	failure string
	why     *apiv1.PreviewFailure
	risk    *apiv1.PreviewRisk
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

// GetPreview answers the last step, with why when it failed.
func (f *fakePreviews) GetPreview(_ context.Context, _ *connect.Request[apiv1.GetPreviewRequest]) (*connect.Response[apiv1.GetPreviewResponse], error) {
	p := f.preview(f.steps[len(f.steps)-1])

	if p.GetStatus() == apiv1.PreviewStatus_PREVIEW_STATUS_FAILED {
		p.Failure = f.why
	}

	if f.risk != nil {
		p.Risk = f.risk
	}

	return connect.NewResponse(&apiv1.GetPreviewResponse{Preview: p}), nil
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

func TestPreviewCreateSaysWhyItFailed(t *testing.T) {
	srv := previewServer(t, &fakePreviews{
		steps:   []apiv1.PreviewStatus{apiv1.PreviewStatus_PREVIEW_STATUS_DEPLOYING, apiv1.PreviewStatus_PREVIEW_STATUS_FAILED},
		failure: `Starting the containers failed: service "migrate" didn't complete successfully: exit 1`,
		why: &apiv1.PreviewFailure{
			Stage:      "start",
			Service:    "migrate",
			Cause:      "migrate couldn't reach its database: the address it connects to has nothing listening.",
			Fix:        "In a preview the database is the compose service's name (`db:5432`, not `localhost:5432`).",
			Confidence: apiv1.PreviewFailureConfidence_PREVIEW_FAILURE_CONFIDENCE_LIKELY,
			ModelName:  "Claude Haiku 5.5",
			Evidence: []*apiv1.PreviewFailureEvidence{
				{Source: "migrate logs", Text: "PostgresError: Connection closed"},
				{Source: "error", Text: `service "migrate" didn't complete successfully: exit 1`},
			},
		},
	})

	out, err := run(t, srv, "--token", "tok-1", "preview", "create", "--repo", "acme-inc/web", "--branch", "main")
	if err == nil {
		t.Fatal("expected an error")
	}

	for _, want := range []string{"Likely cause: migrate couldn't reach its database", "Fix: In a preview", "Explained by Claude Haiku 5.5", "  migrate logs | PostgresError: Connection closed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "error |") {
		t.Errorf("the error is printed twice:\n%s", out)
	}
}

func TestPreviewGetSaysTheMergeRisk(t *testing.T) {
	srv := previewServer(t, &fakePreviews{
		steps: []apiv1.PreviewStatus{apiv1.PreviewStatus_PREVIEW_STATUS_LIVE},
		risk: &apiv1.PreviewRisk{
			Level:     apiv1.PreviewRiskLevel_PREVIEW_RISK_LEVEL_HIGH,
			Summary:   "A NOT NULL on invoices fails on existing rows.",
			Reasons:   []*apiv1.PreviewRiskReason{{Text: "`tenant_id` becomes required.", Files: []string{"migrations/0042.sql"}}},
			Sha:       "3d54034aa1",
			ModelName: "Claude Haiku 5.5",
		},
	})

	out, err := run(t, srv, "--token", "tok-1", "preview", "get", "p-1")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Merge risk: high. A NOT NULL on invoices fails on existing rows.", "  - `tenant_id` becomes required. (migrations/0042.sql)", "Assessed by Claude Haiku 5.5 at 3d54034"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
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

func TestPolicyDurations(t *testing.T) {
	for in, want := range map[string]string{"2h": "2h", "90m": "1h30m", "7d": "7d", "45m": "45m", "default": "24h (Stagd's default)"} {
		d, err := policyDuration(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}

		if got := policySpan(d, "24h"); got != want {
			t.Errorf("%s: shown as %q, want %q", in, got, want)
		}
	}

	for _, in := range []string{"", "soon", "0d", "-1h", "1.5d"} {
		if _, err := policyDuration(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
