package cli

import (
	"context"
	"fmt"
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

// fakeMail is a preview with two captured messages. Its inbox's cursor is
// c-0, and c-n after the nth wait.
type fakeMail struct {
	apiv1connect.UnimplementedPreviewServiceHandler
	cleared bool
	asked   string
	listed  []*apiv1.ListMailRequest
	// What each wait finds, in turn: none once they've run out.
	waits  [][]*apiv1.MailSummary
	waited []*apiv1.WaitForMailRequest
}

var mailAt = timestamppb.New(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))

func (f *fakeMail) ListMail(_ context.Context, req *connect.Request[apiv1.ListMailRequest]) (*connect.Response[apiv1.ListMailResponse], error) {
	if req.Msg.GetPreviewId() != "p-1" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	f.listed = append(f.listed, req.Msg)

	messages := append([]*apiv1.MailSummary{},
		&apiv1.MailSummary{Id: "m-welcome", From: &apiv1.MailAddress{Name: "Acme", Address: "hello@acme.test"}, To: []*apiv1.MailAddress{{Address: "kari@acme.test"}}, Subject: "Welcome to Acme", ReceiveTime: mailAt},
		&apiv1.MailSummary{Id: "m-reset", From: &apiv1.MailAddress{Address: "security@acme.test"}, To: []*apiv1.MailAddress{{Address: "ola@acme.test"}}, Subject: "Reset your password", ReceiveTime: mailAt},
	)

	return connect.NewResponse(&apiv1.ListMailResponse{Total: int32(len(messages)), Messages: messages, Cursor: "c-0"}), nil
}

func (f *fakeMail) WaitForMail(_ context.Context, req *connect.Request[apiv1.WaitForMailRequest]) (*connect.Response[apiv1.WaitForMailResponse], error) {
	f.waited = append(f.waited, req.Msg)

	var found []*apiv1.MailSummary

	if n := len(f.waited); n <= len(f.waits) {
		found = f.waits[n-1]
	}

	return connect.NewResponse(&apiv1.WaitForMailResponse{Messages: found, Cursor: fmt.Sprintf("c-%d", len(f.waited))}), nil
}

func (f *fakeMail) GetMail(_ context.Context, req *connect.Request[apiv1.GetMailRequest]) (*connect.Response[apiv1.GetMailResponse], error) {
	f.asked = req.Msg.GetId()

	return connect.NewResponse(&apiv1.GetMailResponse{Message: &apiv1.MailMessage{
		Summary: &apiv1.MailSummary{Id: req.Msg.GetId(), From: &apiv1.MailAddress{Address: "security@acme.test"}, To: []*apiv1.MailAddress{{Address: "ola@acme.test"}}, Subject: "Reset your password", ReceiveTime: mailAt},
		Text:    "Reset it: https://web.example.test/reset?token=def456",
		Html:    "<p>Reset it</p>",
		Links:   []string{"https://web.example.test/reset?token=def456"},
		Attachments: []*apiv1.MailAttachment{
			{PartId: "2", FileName: "agenda.ics", ContentType: "text/calendar", SizeBytes: 512},
		},
		Unfilled: []*apiv1.UnfilledText{{Part: "subject", Found: "{{name}}", Near: "Hi {{name}}"}},
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

	for _, want := range []string{"Attached: agenda.ics (text/calendar, 512 bytes)", `Unfilled: {{name}} in the subject: "Hi {{name}}"`, "Links:\n  1. https://web.example.test/reset?token=def456"} {
		if !strings.Contains(out, want) {
			t.Errorf("message misses %q:\n%s", want, out)
		}
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

func TestPreviewMailListsWhatMatches(t *testing.T) {
	f := &fakeMail{}
	srv := mailServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "--to", "kari@", "--from", "acme", "--subject", "welcome", "--after", "c-0"); err != nil {
		t.Fatal(err)
	}

	got := f.listed[0]
	if got.GetAfter() != "c-0" || got.GetFilter().GetTo() != "kari@" || got.GetFilter().GetFrom() != "acme" || got.GetFilter().GetSubject() != "welcome" {
		t.Errorf("asked for %v", got)
	}

	out, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "--cursor")
	if err != nil {
		t.Fatal(err)
	}

	if out != "c-0\n" {
		t.Errorf("--cursor printed %q", out)
	}
}

func TestPreviewMailWaitsForTheNextMatchingMessage(t *testing.T) {
	f := &fakeMail{waits: [][]*apiv1.MailSummary{
		nil,
		{
			{Id: "m-confirm-again", To: []*apiv1.MailAddress{{Address: "kari@acme.test"}}, Subject: "Confirm your email", ReceiveTime: mailAt},
			{Id: "m-confirm", To: []*apiv1.MailAddress{{Address: "kari@acme.test"}}, Subject: "Confirm your email", ReceiveTime: mailAt},
		},
	}}
	srv := mailServer(t, f)

	// What's there already doesn't count: the wait starts after the inbox's cursor, and goes on from each answer's.
	if _, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "--wait", "--to", "kari@", "--subject", "confirm"); err != nil {
		t.Fatal(err)
	}

	if len(f.waited) != 2 || f.waited[0].GetAfter() != "c-0" || f.waited[1].GetAfter() != "c-1" {
		t.Fatalf("waited %v", f.waited)
	}

	if w := f.waited[0]; w.GetFilter().GetTo() != "kari@" || w.GetFilter().GetSubject() != "confirm" || w.GetTimeout().AsDuration() != time.Minute {
		t.Errorf("waited for %v", w)
	}

	// Of two that came together, the first the app sent.
	if f.asked != "m-confirm" {
		t.Errorf("printed %q, want m-confirm", f.asked)
	}
}

func TestPreviewMailWaitsAfterTheCursorItsGiven(t *testing.T) {
	f := &fakeMail{waits: [][]*apiv1.MailSummary{{{Id: "m-invite", Subject: "You're invited", ReceiveTime: mailAt}}}}
	srv := mailServer(t, f)

	if _, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "--wait", "--after", "c-before"); err != nil {
		t.Fatal(err)
	}

	if len(f.listed) != 0 || f.waited[0].GetAfter() != "c-before" {
		t.Errorf("listed %d times, waited after %q", len(f.listed), f.waited[0].GetAfter())
	}
}

func TestPreviewMailWaitGivesUp(t *testing.T) {
	srv := mailServer(t, &fakeMail{})

	_, err := run(t, srv, "--token", "tok-1", "preview", "mail", "p-1", "--wait", "--timeout", "30ms")
	if err == nil || !strings.Contains(err.Error(), "no matching mail arrived within 30ms") {
		t.Errorf("want a timeout, got %v", err)
	}
}

func TestPreviewMailNone(t *testing.T) {
	out, err := run(t, mailServer(t, &fakeMail{}), "--token", "tok-1", "preview", "mail", "p-1", "--none", "--to", "kari@", "--timeout", "30ms")
	if err != nil || !strings.Contains(out, "No matching mail arrived within 30ms.") {
		t.Errorf("nothing came: want success, got %v:\n%s", err, out)
	}

	f := &fakeMail{waits: [][]*apiv1.MailSummary{{{Id: "m-oops", To: []*apiv1.MailAddress{{Address: "kari@acme.test"}}, Subject: "Your invoice", ReceiveTime: mailAt}}}}

	_, err = run(t, mailServer(t, f), "--token", "tok-1", "preview", "mail", "p-1", "--none", "--to", "kari@", "--timeout", "30ms")
	if err == nil || !strings.Contains(err.Error(), `mail arrived: "Your invoice" to kari@acme.test`) {
		t.Errorf("one came: want a failure, got %v", err)
	}
}
