package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeActivity has 1200 entries, newest first, and keeps what it was asked.
type fakeActivity struct {
	apiv1connect.UnimplementedActivityServiceHandler

	asked []*apiv1.ListActivityRequest
}

const fakeEntries = 1200

func (f *fakeActivity) ListActivity(_ context.Context, req *connect.Request[apiv1.ListActivityRequest]) (*connect.Response[apiv1.ListActivityResponse], error) {
	f.asked = append(f.asked, req.Msg)

	from := 0

	if req.Msg.GetPageToken() != "" {
		from, _ = strconv.Atoi(req.Msg.GetPageToken())
	}

	size := int(req.Msg.GetPageSize())
	if size == 0 {
		size = 50
	}

	to := min(from+size, fakeEntries)
	res := &apiv1.ListActivityResponse{}

	for i := from; i < to; i++ {
		res.Activity = append(res.Activity, &apiv1.Activity{Id: fmt.Sprint(i), Actor: "ada@acme.example", Via: "web", Action: "secret.set", Target: fmt.Sprintf("S%d in the org", i)})
	}

	if to < fakeEntries {
		res.NextPageToken = strconv.Itoa(to)
	}

	return connect.NewResponse(res), nil
}

func activityServer(t *testing.T, f *fakeActivity) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewActivityServiceHandler(f)
	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestActivityPagesUpToTheLimit(t *testing.T) {
	f := &fakeActivity{}
	srv := activityServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "activity", "--limit", "700", "--area", "Secrets", "--actor", "ada@acme.example")
	if err != nil {
		t.Fatal(err)
	}

	if len(f.asked) != 2 || f.asked[0].GetPageSize() != 500 || f.asked[1].GetPageSize() != 200 || f.asked[1].GetPageToken() != "500" {
		t.Fatalf("asked %v", f.asked)
	}

	if f.asked[1].GetArea() != "secrets" || f.asked[1].GetActor() != "ada@acme.example" {
		t.Fatalf("the filters didn't go with every page: %v", f.asked[1])
	}

	if !strings.Contains(out, "S699 in the org") || strings.Contains(out, "S700 in the org") {
		t.Fatalf("not the first 700")
	}
}

func TestActivityAll(t *testing.T) {
	f := &fakeActivity{}
	srv := activityServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "activity", "--all")
	if err != nil {
		t.Fatal(err)
	}

	if len(f.asked) != 3 || !strings.Contains(out, "S1199 in the org") {
		t.Fatalf("asked %d pages", len(f.asked))
	}
}

func TestActivityUnknownArea(t *testing.T) {
	f := &fakeActivity{}
	srv := activityServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "activity", "--area", "bananas"); err == nil || len(f.asked) != 0 {
		t.Fatal("an unknown area: want an error, and nothing asked")
	}
}
