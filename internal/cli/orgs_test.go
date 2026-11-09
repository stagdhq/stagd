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

	deleted     *apiv1.DeleteOrgRequest
	transferred *apiv1.TransferOwnershipRequest
	renamed     *apiv1.UpdateOrgRequest
}

func (f *fakeOrgs) UpdateOrg(_ context.Context, req *connect.Request[apiv1.UpdateOrgRequest]) (*connect.Response[apiv1.UpdateOrgResponse], error) {
	f.renamed = req.Msg

	return connect.NewResponse(&apiv1.UpdateOrgResponse{Membership: &apiv1.Membership{Org: &apiv1.Org{Slug: "acme", Name: req.Msg.GetName()}, Role: "admin"}}), nil
}

func (f *fakeOrgs) ListMembers(context.Context, *connect.Request[apiv1.ListMembersRequest]) (*connect.Response[apiv1.ListMembersResponse], error) {
	return connect.NewResponse(&apiv1.ListMembersResponse{Members: []*apiv1.Member{
		{User: &apiv1.User{Id: "u1", Email: "ada@acme.example"}, Role: "admin", RoleKind: apiv1.Role_ROLE_ADMIN, Owner: true},
		{User: &apiv1.User{Id: "u2", Email: "bo@acme.example"}, Role: "admin", RoleKind: apiv1.Role_ROLE_ADMIN, Login: "bo"},
		{User: &apiv1.User{Id: "u3", Email: "cy@acme.example"}, Role: "member", RoleKind: apiv1.Role_ROLE_MEMBER},
	}}), nil
}

func (f *fakeOrgs) TransferOwnership(_ context.Context, req *connect.Request[apiv1.TransferOwnershipRequest]) (*connect.Response[apiv1.TransferOwnershipResponse], error) {
	f.transferred = req.Msg

	return connect.NewResponse(&apiv1.TransferOwnershipResponse{}), nil
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

func TestOrgsMembersMarksTheOwner(t *testing.T) {
	srv := orgsServer(t, &fakeOrgs{})

	out, err := run(t, srv, "--token", "tok-1", "orgs", "members")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "admin (owner)") || strings.Count(out, "(owner)") != 1 {
		t.Fatalf("said %q", out)
	}
}

func TestOrgsMembersTransfer(t *testing.T) {
	f := &fakeOrgs{}
	srv := orgsServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "orgs", "members", "transfer", "bo"); err == nil {
		t.Fatal("no --yes and no terminal: want an error")
	}

	if _, err := run(t, srv, "--token", "tok-1", "orgs", "members", "transfer", "--yes", "cy@acme.example"); err == nil {
		t.Fatal("a member, not an admin: want an error")
	}

	if _, err := run(t, srv, "--token", "tok-1", "orgs", "members", "transfer", "--yes", "nobody"); err == nil {
		t.Fatal("nobody of that name: want an error")
	}

	if f.transferred != nil {
		t.Fatal("transferred before it was asked right")
	}

	out, err := run(t, srv, "--token", "tok-1", "orgs", "members", "transfer", "--yes", "bo")
	if err != nil {
		t.Fatal(err)
	}

	if f.transferred.GetUserId() != "u2" || !strings.Contains(out, "bo owns the org now") {
		t.Fatalf("sent %v, said %q", f.transferred, out)
	}
}

func TestOrgsRename(t *testing.T) {
	f := &fakeOrgs{}
	srv := orgsServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "orgs", "rename"); err == nil || f.renamed != nil {
		t.Fatal("no --name and no terminal: want an error, and nothing sent")
	}

	out, err := run(t, srv, "--token", "tok-1", "orgs", "rename", "--name", "  Acme Inc  ")
	if err != nil {
		t.Fatal(err)
	}

	if f.renamed.GetName() != "Acme Inc" || !strings.Contains(out, "Renamed: ") || !strings.Contains(out, "Acme Inc") {
		t.Fatalf("sent %v, said %q", f.renamed, out)
	}
}
