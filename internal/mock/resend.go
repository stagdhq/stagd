package mock

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Resend's API (https://resend.com/docs/api-reference), what its SDKs send
// an email with: POST /emails, POST /emails/batch, and GET /emails/{id}.

// resendList is a field that's an address or a list of them.
type resendList []string

func (l *resendList) UnmarshalJSON(b []byte) error {
	var one string

	if err := json.Unmarshal(b, &one); err == nil {
		if one != "" {
			*l = []string{one}
		}

		return nil
	}

	var many []string

	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}

	*l = many

	return nil
}

// resendContent is an attachment's content: base64, or bytes as Node's Buffer serializes them.
type resendContent []byte

func (c *resendContent) UnmarshalJSON(b []byte) error {
	var text string

	if err := json.Unmarshal(b, &text); err == nil {
		decoded, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			decoded = []byte(text)
		}

		*c = decoded

		return nil
	}

	var buffer struct {
		Data []int `json:"data"`
	}

	var bytes []int

	if err := json.Unmarshal(b, &buffer); err == nil && buffer.Data != nil {
		bytes = buffer.Data
	} else if err := json.Unmarshal(b, &bytes); err != nil {
		return err
	}

	out := make([]byte, len(bytes))

	for i, v := range bytes {
		out[i] = byte(v)
	}

	*c = out

	return nil
}

type resendEmail struct {
	From        string            `json:"from"`
	To          resendList        `json:"to"`
	Cc          resendList        `json:"cc"`
	Bcc         resendList        `json:"bcc"`
	ReplyTo     resendList        `json:"reply_to"`
	Subject     string            `json:"subject"`
	HTML        string            `json:"html"`
	Text        string            `json:"text"`
	Headers     map[string]string `json:"headers"`
	Attachments []struct {
		Filename    string        `json:"filename"`
		Content     resendContent `json:"content"`
		ContentType string        `json:"content_type"`
		Path        string        `json:"path"`
	} `json:"attachments"`
}

// resendError answers as Resend does when an email can't be sent.
func resendError(w http.ResponseWriter, status int, name, message string) {
	writeJSON(w, status, map[string]any{"statusCode": status, "name": name, "message": message})
}

// email turns a request into an Email, or says what's missing, as Resend would.
func (r resendEmail) email() (Email, string) {
	switch {
	case r.From == "":
		return Email{}, "Missing `from` field."
	case len(r.To) == 0:
		return Email{}, "Missing `to` field."
	case r.Subject == "":
		return Email{}, "Missing `subject` field."
	}

	e := Email{Vendor: "resend", From: r.From, To: r.To, Cc: r.Cc, Bcc: r.Bcc, ReplyTo: r.ReplyTo, Subject: r.Subject, Text: r.Text, HTML: r.HTML, Headers: r.Headers}

	for _, a := range r.Attachments {
		content := []byte(a.Content)

		// An attachment Resend would fetch itself: the preview keeps it as a note, not fetching anything.
		if len(content) == 0 && a.Path != "" {
			content = []byte("Resend would attach " + a.Path + "\n")
		}

		e.Attachments = append(e.Attachments, Attachment{Filename: a.Filename, ContentType: a.ContentType, Content: content})
	}

	return e, ""
}

func (s *Server) resend(mux *http.ServeMux) {
	mux.HandleFunc("POST /resend/emails", func(w http.ResponseWriter, r *http.Request) {
		var req resendEmail

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resendError(w, http.StatusUnprocessableEntity, "validation_error", "Invalid JSON: "+err.Error())

			return
		}

		e, problem := req.email()

		if problem != "" {
			resendError(w, http.StatusUnprocessableEntity, "missing_required_field", problem)

			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"id": s.send(e)})
	})

	mux.HandleFunc("POST /resend/emails/batch", func(w http.ResponseWriter, r *http.Request) {
		var reqs []resendEmail

		if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
			resendError(w, http.StatusUnprocessableEntity, "validation_error", "Invalid JSON: "+err.Error())

			return
		}

		// All or none, as Resend's batch is.
		emails := make([]Email, 0, len(reqs))

		for i, req := range reqs {
			e, problem := req.email()

			if problem != "" {
				resendError(w, http.StatusUnprocessableEntity, "missing_required_field", "Email "+strconv.Itoa(i)+": "+problem)

				return
			}

			emails = append(emails, e)
		}

		data := make([]map[string]string, 0, len(emails))

		for _, e := range emails {
			data = append(data, map[string]string{"id": s.send(e)})
		}

		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	})

	mux.HandleFunc("GET /resend/emails/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		s.mu.Lock()
		e, ok := s.sent[id]
		at := s.sentAt[id]
		_, failed := s.failures[id]
		s.mu.Unlock()

		if !ok {
			resendError(w, http.StatusNotFound, "not_found", "Email not found")

			return
		}

		event := "delivered"

		if failed {
			event = "failed"
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"object":     "email",
			"id":         id,
			"from":       e.From,
			"to":         e.To,
			"cc":         nonNil(e.Cc),
			"bcc":        nonNil(e.Bcc),
			"reply_to":   nonNil(e.ReplyTo),
			"subject":    e.Subject,
			"html":       e.HTML,
			"text":       e.Text,
			"created_at": at.UTC().Format(time.RFC3339Nano),
			"last_event": event,
		})
	})
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}

	return l
}
