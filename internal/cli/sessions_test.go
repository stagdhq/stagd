package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeAccount has this CLI's session (s-cli) and a browser's (s-web), and a
// PreviewService and ScmService for the lists' --limit.
type fakeAccount struct {
	apiv1connect.UnimplementedAccountServiceHandler
	apiv1connect.UnimplementedPreviewServiceHandler

	revoked      []string
	othersGone   bool
	listPageSize int32

	// When set, how many previews there are, served a page at a time: the page token is how many came before.
	stored int
	pages  []string
}

func (f *fakeAccount) ListSessions(context.Context, *connect.Request[apiv1.ListSessionsRequest]) (*connect.Response[apiv1.ListSessionsResponse], error) {
	at := timestamppb.New(time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC))

	return connect.NewResponse(&apiv1.ListSessionsResponse{Sessions: []*apiv1.Session{
		{
			Id: "s-cli", Kind: apiv1.SessionKind_SESSION_KIND_CLI, DeviceName: "karis-mac", ClientPlatform: "darwin/arm64", ClientVersion: "0.9.0",
			SignInMethod: "device", LastSeenTime: at, ExpireTime: at, Current: true,
		},
		{
			Id: "s-web", Kind: apiv1.SessionKind_SESSION_KIND_WEB, SignInMethod: "github", LastSeenTime: at, ExpireTime: at,
			UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		},
	}}), nil
}

func (f *fakeAccount) RevokeSession(_ context.Context, req *connect.Request[apiv1.RevokeSessionRequest]) (*connect.Response[apiv1.RevokeSessionResponse], error) {
	f.revoked = append(f.revoked, req.Msg.GetId())

	return connect.NewResponse(&apiv1.RevokeSessionResponse{}), nil
}

func (f *fakeAccount) RevokeOtherSessions(context.Context, *connect.Request[apiv1.RevokeOtherSessionsRequest]) (*connect.Response[apiv1.RevokeOtherSessionsResponse], error) {
	f.othersGone = true

	return connect.NewResponse(&apiv1.RevokeOtherSessionsResponse{Revoked: 1}), nil
}

func (f *fakeAccount) ListPreviews(_ context.Context, req *connect.Request[apiv1.ListPreviewsRequest]) (*connect.Response[apiv1.ListPreviewsResponse], error) {
	f.listPageSize = req.Msg.GetPageSize()
	f.pages = append(f.pages, req.Msg.GetPageToken())

	n := int(req.Msg.GetPageSize())
	next := ""

	if f.stored > 0 {
		from, _ := strconv.Atoi(req.Msg.GetPageToken())
		n = min(n, f.stored-from)

		if from+n < f.stored {
			next = strconv.Itoa(from + n)
		}
	}

	var previews []*apiv1.Preview

	for range n {
		previews = append(previews, &apiv1.Preview{Id: "p", Repository: &apiv1.Repository{FullName: "acme-inc/web"}, Change: &apiv1.ChangeRequest{Number: 1}})
	}

	return connect.NewResponse(&apiv1.ListPreviewsResponse{Previews: previews, NextPageToken: next}), nil
}

func accountServer(t *testing.T, f *fakeAccount) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	path, handler := apiv1connect.NewAccountServiceHandler(f)
	mux.Handle(path, handler)

	path, handler = apiv1connect.NewPreviewServiceHandler(f)
	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestAuthSessionsSaysWhereAndWhichIsThisOne(t *testing.T) {
	srv := accountServer(t, &fakeAccount{})

	out, err := run(t, srv, "--token", "tok-1", "auth", "sessions")
	if err != nil {
		t.Fatalf("auth sessions: %v\n%s", err, out)
	}

	for _, want := range []string{"karis-mac · darwin/arm64 · 0.9.0", "s-cli (this one)", "Chrome on macOS", "github", "s-web"} {
		if !strings.Contains(out, want) {
			t.Errorf("auth sessions misses %q:\n%s", want, out)
		}
	}
}

func TestAuthRevoke(t *testing.T) {
	f := &fakeAccount{}
	srv := accountServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "auth", "revoke"); err == nil {
		t.Error("revoke without an id or --others should refuse")
	}

	out, err := run(t, srv, "--token", "tok-1", "auth", "revoke", "s-web")
	if err != nil || !strings.Contains(out, "Signed out session s-web") || len(f.revoked) != 1 || f.revoked[0] != "s-web" {
		t.Fatalf("revoke s-web: %v, revoked %v\n%s", err, f.revoked, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "auth", "revoke", "--others")
	if err != nil || !f.othersGone || !strings.Contains(out, "1 other session") {
		t.Fatalf("revoke --others: %v\n%s", err, out)
	}
}

func TestAuthRevokeThisSessionForgetsTheToken(t *testing.T) {
	srv := accountServer(t, &fakeAccount{})

	if err := store.Save(srv.URL, "tok-1"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, srv, "auth", "revoke", "s-cli")
	if err != nil || !strings.Contains(out, "that was this session") {
		t.Fatalf("revoke s-cli: %v\n%s", err, out)
	}

	if got, _ := store.Load(srv.URL); got != "" {
		t.Errorf("the stored token stayed: %q", got)
	}
}

func TestLogoutAllSignsOutTheOthersFirst(t *testing.T) {
	f := &fakeAccount{}
	srv := accountServer(t, f)

	if err := store.Save(srv.URL, "tok-1"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, srv, "logout", "--all")
	if err != nil || !f.othersGone || !strings.Contains(out, "Signed out of 1 other session") || !strings.Contains(out, "Signed out.") {
		t.Fatalf("logout --all: %v\n%s", err, out)
	}

	if got, _ := store.Load(srv.URL); got != "" {
		t.Errorf("the stored token stayed: %q", got)
	}
}

func TestPreviewListLimit(t *testing.T) {
	f := &fakeAccount{}
	srv := accountServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "preview", "list", "--limit", "3"); err != nil || f.listPageSize != 3 {
		t.Fatalf("preview list --limit 3: %v, asked for %d", err, f.listPageSize)
	}

	if _, err := run(t, srv, "--token", "tok-1", "preview", "list"); err != nil || f.listPageSize != 50 {
		t.Fatalf("preview list: %v, asked for %d", err, f.listPageSize)
	}

	if _, err := run(t, srv, "--token", "tok-1", "preview", "list", "--limit", "6000"); err == nil {
		t.Error("--limit 6000 should refuse: stagd lists 5000 at most")
	}
}

func TestPreviewListPages(t *testing.T) {
	f := &fakeAccount{stored: 450}
	srv := accountServer(t, f)

	// 450 of them, asked for at most 500: three pages, the last one short, and nothing said about more.
	out, err := run(t, srv, "--token", "tok-1", "preview", "list", "--limit", "500", "--json")
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Count(out, `"id"`); got != 450 {
		t.Errorf("listed %d, want 450", got)
	}

	if want := []string{"", "200", "400"}; !slices.Equal(f.pages, want) {
		t.Errorf("asked for pages %q, want %q", f.pages, want)
	}

	// Asked for fewer than there are: it stops there.
	f.pages = nil

	if _, err := run(t, srv, "--token", "tok-1", "preview", "list", "--limit", "250"); err != nil {
		t.Fatal(err)
	}

	if f.listPageSize != 50 || len(f.pages) != 2 {
		t.Errorf("asked for %d pages, the last of %d: want 2, the last of 50", len(f.pages), f.listPageSize)
	}
}

func TestReposListLimit(t *testing.T) {
	srv := scmServer(t, &fakeScm{})

	out, err := run(t, srv, "--token", "tok-1", "repos", "list", "--all", "--limit", "1")
	if err != nil {
		t.Fatalf("repos list --limit 1: %v\n%s", err, out)
	}

	// By name: .github before web.
	if !strings.Contains(out, "acme-inc/.github") || strings.Contains(out, "acme-inc/web") || !strings.Contains(out, "1 more") {
		t.Errorf("repos list --limit 1:\n%s", out)
	}
}
