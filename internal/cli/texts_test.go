package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeTexts is a preview with one text message caught. Its cursor is t-0,
// and t-n after the nth wait.
type fakeTexts struct {
	apiv1connect.UnimplementedPreviewServiceHandler

	listed []*apiv1.ListTextsRequest
	// What each wait finds, in turn: none once they've run out.
	waits  [][]*apiv1.TextMessage
	waited []*apiv1.WaitForTextsRequest
}

func (f *fakeTexts) ListTexts(_ context.Context, req *connect.Request[apiv1.ListTextsRequest]) (*connect.Response[apiv1.ListTextsResponse], error) {
	if req.Msg.GetPreviewId() != "p-1" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	f.listed = append(f.listed, req.Msg)

	texts := []*apiv1.TextMessage{{Id: "t-welcome", Vendor: "sns", To: "+4791234567", Body: "Welcome to\\nAcme!", SendTime: mailAt}}

	return connect.NewResponse(&apiv1.ListTextsResponse{Texts: texts, Cursor: "t-0"}), nil
}

func (f *fakeTexts) WaitForTexts(_ context.Context, req *connect.Request[apiv1.WaitForTextsRequest]) (*connect.Response[apiv1.WaitForTextsResponse], error) {
	f.waited = append(f.waited, req.Msg)

	var found []*apiv1.TextMessage

	if n := len(f.waited); n <= len(f.waits) {
		found = f.waits[n-1]
	}

	return connect.NewResponse(&apiv1.WaitForTextsResponse{Texts: found, Cursor: fmt.Sprintf("t-%d", len(f.waited))}), nil
}

func textsServer(t *testing.T, f *fakeTexts) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)

	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)

	t.Cleanup(srv.Close)

	return srv
}

func TestPreviewTextsLists(t *testing.T) {
	out, err := run(t, textsServer(t, &fakeTexts{}), "--token", "tok-1", "preview", "texts", "p-1")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "+4791234567") || !strings.Contains(out, "Welcome to") {
		t.Errorf("output:\n%s", out)
	}
}

func TestPreviewTextsListsWhatMatches(t *testing.T) {
	f := &fakeTexts{}

	if _, err := run(t, textsServer(t, f), "--token", "tok-1", "preview", "texts", "p-1", "--to", "+47 912", "--body", "welcome", "--after", "t-0"); err != nil {
		t.Fatal(err)
	}

	if got := f.listed[0]; got.GetAfter() != "t-0" || got.GetFilter().GetTo() != "+47 912" || got.GetFilter().GetBody() != "welcome" {
		t.Errorf("asked for %v", got)
	}
}

func TestPreviewTextsWaitsForTheNextOneToANumber(t *testing.T) {
	f := &fakeTexts{waits: [][]*apiv1.TextMessage{
		nil,
		{{Id: "t-code", To: "+4791234567", Body: "Your code is 123456", SendTime: mailAt}},
	}}

	// The welcome text is already there: the wait starts after the cursor, and the number is the server's to match.
	out, err := run(t, textsServer(t, f), "--token", "tok-1", "preview", "texts", "p-1", "--wait", "--to", "91234567")
	if err != nil {
		t.Fatal(err)
	}

	if strings.TrimSpace(out) != "Your code is 123456" {
		t.Errorf("printed %q", out)
	}

	if len(f.waited) != 2 || f.waited[0].GetAfter() != "t-0" || f.waited[1].GetAfter() != "t-1" || f.waited[0].GetFilter().GetTo() != "91234567" {
		t.Errorf("waited %v", f.waited)
	}
}

func TestPreviewTextsNone(t *testing.T) {
	f := &fakeTexts{waits: [][]*apiv1.TextMessage{{{Id: "t-oops", To: "+4791234567", Body: "Your order\nhas shipped", SendTime: mailAt}}}}

	_, err := run(t, textsServer(t, f), "--token", "tok-1", "preview", "texts", "p-1", "--none", "--timeout", "30ms")
	if err == nil || !strings.Contains(err.Error(), `a text message arrived: "Your order has shipped" to +4791234567`) {
		t.Errorf("one came: want a failure, got %v", err)
	}
}
