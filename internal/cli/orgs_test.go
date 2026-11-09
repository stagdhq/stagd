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

// fakeOrgs takes a DeleteOrg and says a Kubernetes cluster still has Stagd's access.
type fakeOrgs struct {
	apiv1connect.UnimplementedOrgServiceHandler

	deleted *apiv1.DeleteOrgRequest
}

func (f *fakeOrgs) DeleteOrg(_ context.Context, req *connect.Request[apiv1.DeleteOrgRequest]) (*connect.Response[apiv1.DeleteOrgResponse], error) {
	f.deleted = req.Msg

	return connect.NewResponse(&apiv1.DeleteOrgResponse{Compute: []string{"kind"}}), nil
}

func orgsServer(t *testing.T, f *fakeOrgs) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	path, handler := apiv1connect.NewOrgServiceHandler(f)
	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestOrgsDelete(t *testing.T) {
	f := &fakeOrgs{}
	srv := orgsServer(t, f)

	// The slug is required, and --yes without anyone at the keyboard.
	if _, err := run(t, srv, "--token", "tok-1", "orgs", "delete"); err == nil {
		t.Fatal("no slug: want an error")
	}

	if _, err := run(t, srv, "--token", "tok-1", "orgs", "delete", "acme"); err == nil {
		t.Fatal("no --yes and no terminal: want an error")
	}

	if _, err := run(t, srv, "--token", "tok-1", "orgs", "delete", "acme", "--yes", "--reason", "bored"); err == nil {
		t.Fatal("an unknown --reason: want an error")
	}

	if f.deleted != nil {
		t.Fatal("deleted before it was confirmed")
	}

	out, err := run(t, srv, "--token", "tok-1", "orgs", "delete", "acme", "--yes", "--reason", "just-trying", "--why", "only had a look")
	if err != nil {
		t.Fatal(err)
	}

	if f.deleted.GetSlug() != "acme" || f.deleted.GetReason() != apiv1.LeaveReason_LEAVE_REASON_JUST_TRYING || f.deleted.GetReasonText() != "only had a look" {
		t.Fatalf("sent %v", f.deleted)
	}

	if !strings.Contains(out, "Deleted acme.") || !strings.Contains(out, "still installed on your side for: kind") {
		t.Fatalf("said %q", out)
	}
}
