package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeScm is a ScmService whose connect stays pending for `pendingPolls`
// polls, then ends with `final`.
type fakeScm struct {
	apiv1connect.UnimplementedScmServiceHandler
	pendingPolls int
	final        *apiv1.GetConnectStatusResponse
	polls        int
	deleted      string
	enabled      map[string]bool
	hidden       map[string]bool
}

var acmeConnection = &apiv1.Connection{
	Id:        "c-1",
	Provider:  apiv1.ScmProvider_SCM_PROVIDER_GITHUB,
	Account:   "acme-inc",
	Status:    apiv1.ConnectionStatus_CONNECTION_STATUS_ACTIVE,
	CreatedBy: &apiv1.User{Email: "kari@acme.test"},
}

func (f *fakeScm) StartConnect(_ context.Context, req *connect.Request[apiv1.StartConnectRequest]) (*connect.Response[apiv1.StartConnectResponse], error) {
	if req.Msg.GetProvider() != apiv1.ScmProvider_SCM_PROVIDER_GITHUB {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	return connect.NewResponse(&apiv1.StartConnectResponse{
		Url:        "https://github.test/apps/stagd/installations/new?state=s-1",
		State:      "s-1",
		ExpireTime: timestamppb.New(time.Now().Add(time.Minute)),
	}), nil
}

func (f *fakeScm) GetConnectStatus(_ context.Context, req *connect.Request[apiv1.GetConnectStatusRequest]) (*connect.Response[apiv1.GetConnectStatusResponse], error) {
	if req.Msg.GetState() != "s-1" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	f.polls++

	if f.polls <= f.pendingPolls {
		return connect.NewResponse(&apiv1.GetConnectStatusResponse{Status: apiv1.ConnectStatus_CONNECT_STATUS_PENDING}), nil
	}

	return connect.NewResponse(f.final), nil
}

func (f *fakeScm) ListConnections(context.Context, *connect.Request[apiv1.ListConnectionsRequest]) (*connect.Response[apiv1.ListConnectionsResponse], error) {
	return connect.NewResponse(&apiv1.ListConnectionsResponse{Connections: []*apiv1.Connection{acmeConnection}}), nil
}

func (f *fakeScm) DeleteConnection(_ context.Context, req *connect.Request[apiv1.DeleteConnectionRequest]) (*connect.Response[apiv1.DeleteConnectionResponse], error) {
	f.deleted = req.Msg.GetId()

	return connect.NewResponse(&apiv1.DeleteConnectionResponse{}), nil
}

func (f *fakeScm) SetPreviewsEnabled(_ context.Context, req *connect.Request[apiv1.SetPreviewsEnabledRequest]) (*connect.Response[apiv1.SetPreviewsEnabledResponse], error) {
	f.enabled = map[string]bool{req.Msg.GetRepository(): req.Msg.GetEnabled()}
	destroyed := int32(0)
	if !req.Msg.GetEnabled() {
		destroyed = 2
	}

	return connect.NewResponse(&apiv1.SetPreviewsEnabledResponse{Repository: &apiv1.Repository{FullName: req.Msg.GetRepository()}, Destroyed: destroyed}), nil
}

func (f *fakeScm) ListRepositories(_ context.Context, req *connect.Request[apiv1.ListRepositoriesRequest]) (*connect.Response[apiv1.ListRepositoriesResponse], error) {
	repos := []*apiv1.Repository{
		{Id: "r-1", ConnectionId: "c-1", Provider: apiv1.ScmProvider_SCM_PROVIDER_GITHUB, FullName: "acme-inc/web", DefaultBranch: "main", Private: true},
		{Id: "r-2", ConnectionId: "c-1", Provider: apiv1.ScmProvider_SCM_PROVIDER_GITHUB, FullName: "acme-inc/.github", DefaultBranch: "main", Hidden: f.hidden["acme-inc/.github"]},
	}

	if req.Msg.GetConnectionId() != "" && req.Msg.GetConnectionId() != "c-1" {
		repos = nil
	}

	return connect.NewResponse(&apiv1.ListRepositoriesResponse{Repositories: repos}), nil
}

func (f *fakeScm) SetRepositoryHidden(_ context.Context, req *connect.Request[apiv1.SetRepositoryHiddenRequest]) (*connect.Response[apiv1.SetRepositoryHiddenResponse], error) {
	if f.hidden == nil {
		f.hidden = map[string]bool{}
	}

	f.hidden[req.Msg.GetRepository()] = req.Msg.GetHidden()

	return connect.NewResponse(&apiv1.SetRepositoryHiddenResponse{Repository: &apiv1.Repository{FullName: req.Msg.GetRepository(), Hidden: req.Msg.GetHidden()}}), nil
}

func scmServer(t *testing.T, scm *fakeScm) *httptest.Server {
	t.Helper()
	connectPollInterval = time.Millisecond

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewScmServiceHandler(scm)
	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestGithubConnectWaitsUntilConnected(t *testing.T) {
	scm := &fakeScm{
		pendingPolls: 2,
		final:        &apiv1.GetConnectStatusResponse{Status: apiv1.ConnectStatus_CONNECT_STATUS_CONNECTED, Connection: acmeConnection},
	}
	srv := scmServer(t, scm)

	out, err := run(t, srv, "--token", "tok-1", "github", "connect", "--no-browser")
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, out)
	}

	for _, want := range []string{"installations/new?state=s-1", "Connected GitHub account acme-inc"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}

	if scm.polls != 3 {
		t.Errorf("polled %d times, want 3", scm.polls)
	}
}

func TestGithubConnectReportsFailure(t *testing.T) {
	srv := scmServer(t, &fakeScm{final: &apiv1.GetConnectStatusResponse{
		Status: apiv1.ConnectStatus_CONNECT_STATUS_FAILED,
		Error:  "acme-inc is already connected to another Stagd org.",
	}})

	_, err := run(t, srv, "--token", "tok-1", "github", "connect", "--no-browser")
	if err == nil || !strings.Contains(err.Error(), "another Stagd org") {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectionsAndRepos(t *testing.T) {
	scm := &fakeScm{}
	srv := scmServer(t, scm)

	out, err := run(t, srv, "--token", "tok-1", "connections", "list")
	if err != nil || !strings.Contains(out, "acme-inc") || !strings.Contains(out, "github") || !strings.Contains(out, "kari@acme.test") {
		t.Fatalf("connections list: %v\n%s", err, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "repos", "list")
	if err != nil || !strings.Contains(out, "acme-inc/web") || !strings.Contains(out, "private") {
		t.Fatalf("repos list: %v\n%s", err, out)
	}

	if _, err := run(t, srv, "--token", "tok-1", "connections", "delete", "c-1"); err == nil {
		t.Fatal("delete without --yes and without a terminal should refuse")
	}

	if _, err := run(t, srv, "--token", "tok-1", "connections", "delete", "--yes", "c-1"); err != nil || scm.deleted != "c-1" {
		t.Fatalf("delete: %v, deleted %q", err, scm.deleted)
	}
}

func TestReposEnableAndDisable(t *testing.T) {
	scm := &fakeScm{}
	srv := scmServer(t, scm)

	out, err := run(t, srv, "--token", "tok-1", "repos", "enable", "acme-inc/web")
	if err != nil || !scm.enabled["acme-inc/web"] || !strings.Contains(out, "Previews are on for acme-inc/web") {
		t.Fatalf("enable: %v %v\n%s", err, scm.enabled, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "repos", "disable", "acme-inc/web")
	if err != nil || scm.enabled["acme-inc/web"] || !strings.Contains(out, "Tearing down 2") {
		t.Fatalf("disable: %v\n%s", err, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "repos", "list")
	if err != nil || !strings.Contains(out, "PREVIEWS") {
		t.Fatalf("list: %v\n%s", err, out)
	}
}

func TestReposHideAndUnhide(t *testing.T) {
	scm := &fakeScm{}
	srv := scmServer(t, scm)

	out, err := run(t, srv, "--token", "tok-1", "repos", "hide", "acme-inc/.github")
	if err != nil || !scm.hidden["acme-inc/.github"] || !strings.Contains(out, "acme-inc/.github is hidden") {
		t.Fatalf("hide: %v %v\n%s", err, scm.hidden, out)
	}

	// Left out of the list, and said so.
	out, err = run(t, srv, "--token", "tok-1", "repos", "list")
	if err != nil || strings.Contains(out, "acme-inc/.github") || !strings.Contains(out, "1 hidden; see them with stagd repos list --all") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "repos", "list", "--all")
	if err != nil || !strings.Contains(out, "acme-inc/.github") || !strings.Contains(out, "(hidden)") {
		t.Fatalf("list --all: %v\n%s", err, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "repos", "unhide", "acme-inc/.github")
	if err != nil || scm.hidden["acme-inc/.github"] || !strings.Contains(out, "in the org's lists again") {
		t.Fatalf("unhide: %v\n%s", err, out)
	}
}
