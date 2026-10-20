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

	apiv1 "stagd.dev/stagd/internal/gen/stagd/api/v1"
	"stagd.dev/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeMail is a preview with two captured messages.
type fakeMail struct {
	apiv1connect.UnimplementedPreviewServiceHandler
	cleared bool
	asked   string
	// Messages that arrive one per list call, after the first.
	arriving []*apiv1.MailSummary
	lists    int
	arrived  []*apiv1.MailSummary
}

var mailAt = timestamppb.New(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))

func (f *fakeMail) ListMail(_ context.Context, req *connect.Request[apiv1.ListMailRequest]) (*connect.Response[apiv1.ListMailResponse], error) {
	if req.Msg.GetPreviewId() != "p-1" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	f.lists++
	if f.lists > 1 && len(f.arriving) > 0 {
		f.arrived = append([]*apiv1.MailSummary{f.arriving[0]}, f.arrived...)
		f.arriving = f.arriving[1:]
	}

	messages := append(append([]*apiv1.MailSummary{}, f.arrived...),
		&apiv1.MailSummary{Id: "m-welcome", From: &apiv1.MailAddress{Name: "Acme", Address: "hello@acme.test"}, To: []*apiv1.MailAddress{{Address: "kari@acme.test"}}, Subject: "Welcome to Acme", ReceiveTime: mailAt},
		&apiv1.MailSummary{Id: "m-reset", From: &apiv1.MailAddress{Address: "security@acme.test"}, To: []*apiv1.MailAddress{{Address: "ola@acme.test"}}, Subject: "Reset your password", ReceiveTime: mailAt},
	)

	return connect.NewResponse(&apiv1.ListMailResponse{Total: int32(len(messages)), Messages: messages}), nil
}

func (f *fakeMail) GetMail(_ context.Context, req *connect.Request[apiv1.GetMailRequest]) (*connect.Response[apiv1.GetMailResponse], error) {
	f.asked = req.Msg.GetId()

	return connect.NewResponse(&apiv1.GetMailResponse{Message: &apiv1.MailMessage{
		Summary: &apiv1.MailSummary{Id: req.Msg.GetId(), From: &apiv1.MailAddress{Address: "security@acme.test"}, To: []*apiv1.MailAddress{{Address: "ola@acme.test"}}, Subject: "Reset your password", ReceiveTime: mailAt},
		Text:    "Reset it: https://web.example.test/reset?token=def456",
		Html:    "<p>Reset it</p>",
	}}), nil
}

func (f *fakeMail) ClearMail(context.Context, *connect.Request[apiv1.ClearMailRequest]) (*connect.Response[apiv1.ClearMailResponse], error) {
	f.cleared = true

	return connect.NewResponse(&apiv1.ClearMailResponse{}), nil
}

func mailServer(t *testing.T, f *fakeMail) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestPreviewMailListsAndOpensByNumber(t *testing.T) {
	f := &fakeMail{}
	srv := mailServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Welcome to Acme", "Reset your password", "Acme <hello@acme.test>"} {
		if !strings.Contains(out, want) {
			t.Errorf("list misses %q:\n%s", want, out)
		}
	}

	out, err = run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "2")
	if err != nil {
		t.Fatal(err)
	}

	if f.asked != "m-reset" || !strings.Contains(out, "reset?token=def456") || !strings.Contains(out, "captured, not delivered") {
		t.Errorf("opened %q:\n%s", f.asked, out)
	}

	out, _ = run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "2", "--html")
	if !strings.Contains(out, "<p>Reset it</p>") {
		t.Errorf("--html prints the HTML:\n%s", out)
	}

	if _, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "9"); err == nil || !strings.Contains(err.Error(), "there's no message 9") {
		t.Errorf("an out-of-range number says so, got %v", err)
	}
}

func TestPreviewMailClear(t *testing.T) {
	f := &fakeMail{}
	srv := mailServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "--clear"); err != nil {
		t.Fatal(err)
	}

	if !f.cleared {
		t.Error("--clear didn't clear")
	}
}

func TestPreviewMailWaitsForTheNextMatchingMessage(t *testing.T) {
	mailPoll = time.Millisecond
	t.Cleanup(func() { mailPoll = 2 * time.Second })

	f := &fakeMail{arriving: []*apiv1.MailSummary{
		{Id: "m-other", To: []*apiv1.MailAddress{{Address: "hank@globex.test"}}, Subject: "Confirm your email", ReceiveTime: mailAt},
		{Id: "m-confirm", To: []*apiv1.MailAddress{{Address: "kari@acme.test"}}, Subject: "Confirm your email", ReceiveTime: mailAt},
	}}
	srv := mailServer(t, f)

	// The welcome mail to kari is already there: only new mail counts. The first new one is to someone else.
	if _, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "--wait", "--to", "kari@", "--subject", "confirm"); err != nil {
		t.Fatal(err)
	}

	if f.asked != "m-confirm" {
		t.Errorf("printed %q, want m-confirm", f.asked)
	}
}

func TestPreviewMailWaitGivesUp(t *testing.T) {
	mailPoll = time.Millisecond
	t.Cleanup(func() { mailPoll = 2 * time.Second })

	srv := mailServer(t, &fakeMail{})

	_, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "--wait", "--timeout", "30ms")
	if err == nil || !strings.Contains(err.Error(), "no matching mail arrived within 30ms") {
		t.Errorf("want a timeout, got %v", err)
	}
}
