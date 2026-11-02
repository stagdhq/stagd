// Package mock answers vendors' sending APIs the way the vendors do, so an
// app in a preview can send email and text messages without them leaving
// the preview: emails go on to the preview's Mailpit over SMTP, text
// messages are kept here. It's `stagd mock`, which Stagd runs in previews
// with mail capture as the built-in stagd-mock.
//
// Apps reach it without code changes where the vendor's SDK reads its
// address from the environment (Stagd sets them in previews):
//
//	Resend:      RESEND_BASE_URL (Node, Go, Ruby, PHP), RESEND_API_URL (Python): http://stagd-mock:8080/resend
//	Amazon SES:  AWS_ENDPOINT_URL_SES, AWS_ENDPOINT_URL_SESV2: http://stagd-mock:4566
//	Amazon SNS:  AWS_ENDPOINT_URL_SNS: http://stagd-mock:4566 (text messages)
//
// AWS has a port of its own: its SDKs take an endpoint as a host, and SES's
// first API and SNS both answer at /.
package mock

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Server answers the vendors' APIs and passes what they were asked to send on.
type Server struct {
	// SMTP is where emails go, host:port (the preview's Mailpit); empty logs them only.
	SMTP string

	Log *slog.Logger

	mu       sync.Mutex
	texts    []Text
	sent     map[string]Email
	sentAt   map[string]time.Time
	failures map[string]string
}

// Text is a text message (SMS) the app sent.
type Text struct {
	ID     string    `json:"id"`
	Vendor string    `json:"vendor"`
	To     string    `json:"to"`
	From   string    `json:"from,omitempty"`
	Body   string    `json:"body"`
	Time   time.Time `json:"time"`
}

// maxTexts is how many text messages are kept, newest first out last.
const maxTexts = 500

func (s *Server) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return s.Log
}

// send passes an Email on to SMTP, or logs it; it never fails the app's call: what the vendor would have accepted, the mock accepts.
func (s *Server) send(e Email) string {
	id := uuid()

	s.mu.Lock()

	if s.sent == nil {
		s.sent, s.sentAt, s.failures = map[string]Email{}, map[string]time.Time{}, map[string]string{}
	}

	s.sent[id] = e
	s.sentAt[id] = time.Now()
	s.mu.Unlock()

	attrs := []any{"vendor", e.Vendor, "id", id, "to", e.recipients(), "subject", e.Subject}

	if s.SMTP == "" {
		s.log().Info("mock: email kept here (no SMTP to pass it to)", attrs...)

		return id
	}

	if err := deliver(s.SMTP, e, id); err != nil {
		s.mu.Lock()
		s.failures[id] = err.Error()
		s.mu.Unlock()

		s.log().Warn("mock: email not passed on to SMTP", append(attrs, "error", err)...)

		return id
	}

	s.log().Info("mock: email caught", attrs...)

	return id
}

// text keeps a text message.
func (s *Server) text(t Text) string {
	t.ID = newID()
	t.Time = time.Now().UTC()

	s.mu.Lock()
	s.texts = append([]Text{t}, s.texts...)

	if len(s.texts) > maxTexts {
		s.texts = s.texts[:maxTexts]
	}

	s.mu.Unlock()

	s.log().Info("mock: text message caught", "vendor", t.Vendor, "id", t.ID, "to", t.To)

	return t.ID
}

// Texts is the text messages caught, newest first.
func (s *Server) Texts() []Text {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Text(nil), s.texts...)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Handler answers the vendors with JSON APIs, each under its own path (/resend/…), and the mock's own: /healthz and /texts.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})

	mux.HandleFunc("GET /texts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"texts": s.Texts()})
	})

	s.resend(mux)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": fmt.Sprintf("stagd mock: nothing answers %s %s", r.Method, r.URL.Path)})
	})

	return mux
}
