package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeTexts is a preview with one text message caught, and more that arrive one per list call after the first.
type fakeTexts struct {
	apiv1connect.UnimplementedPreviewServiceHandler

	arriving []*apiv1.TextMessage
	arrived  []*apiv1.TextMessage
	lists    int
}

func (f *fakeTexts) ListTexts(_ context.Context, req *connect.Request[apiv1.ListTextsRequest]) (*connect.Response[apiv1.ListTextsResponse], error) {
	if req.Msg.GetPreviewId() != "p-1" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	f.lists++

	if f.lists > 1 && len(f.arriving) > 0 {
		f.arrived = append([]*apiv1.TextMessage{f.arriving[0]}, f.arrived...)
		f.arriving = f.arriving[1:]
	}

	texts := append(append([]*apiv1.TextMessage{}, f.arrived...), &apiv1.TextMessage{Id: "t-welcome", Vendor: "sns", To: "+4791234567", Body: "Welcome to\\nAcme!", SendTime: mailAt})

	return connect.NewResponse(&apiv1.ListTextsResponse{Texts: texts}), nil
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

func TestPreviewTextsWaitsForTheNextOneToANumber(t *testing.T) {
	mailPoll = time.Millisecond
	t.Cleanup(func() { mailPoll = 2 * time.Second })

	f := &fakeTexts{arriving: []*apiv1.TextMessage{
		{Id: "t-other", To: "+4799999999", Body: "Your code is 111111", SendTime: mailAt},
		{Id: "t-code", To: "+4791234567", Body: "Your code is 123456", SendTime: mailAt},
	}}

	// The welcome text to the number is already there: only new ones count, and the first new one is to another number.
	out, err := run(t, textsServer(t, f), "--token", "tok-1", "preview", "texts", "p-1", "--wait", "--to", "91234567")
	if err != nil {
		t.Fatal(err)
	}

	if strings.TrimSpace(out) != "Your code is 123456" {
		t.Errorf("printed %q", out)
	}
}
