package mock

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// received is one message an SMTP server got.
type received struct {
	from string
	to   []string
	msg  *mail.Message
	raw  string
}

// smtpServer is just enough SMTP for net/smtp: it keeps what it's sent.
func smtpServer(t *testing.T) (string, func() []received) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = l.Close() })

	var mu sync.Mutex
	var got []received

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}

			go func() {
				defer conn.Close()

				r := bufio.NewReader(conn)
				say := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }

				say("220 test")

				var cur received

				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}

					cmd := strings.ToUpper(strings.TrimSpace(line))

					switch {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						say("250 test")
					case strings.HasPrefix(cmd, "MAIL FROM:"):
						cur.from = strings.Trim(strings.TrimSpace(line)[10:], "<>")
						say("250 ok")
					case strings.HasPrefix(cmd, "RCPT TO:"):
						cur.to = append(cur.to, strings.Trim(strings.TrimSpace(line)[8:], "<>"))
						say("250 ok")
					case cmd == "DATA":
						say("354 go on")

						var data strings.Builder

						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}

							data.WriteString(strings.TrimPrefix(l, "."))
						}

						cur.raw = data.String()
						cur.msg, _ = mail.ReadMessage(strings.NewReader(cur.raw))

						mu.Lock()
						got = append(got, cur)
						mu.Unlock()

						cur = received{}

						say("250 queued")
					case cmd == "QUIT":
						say("221 bye")

						return
					default:
						say("250 ok")
					}
				}
			}()
		}
	}()

	return l.Addr().String(), func() []received {
		mu.Lock()
		defer mu.Unlock()

		return append([]received(nil), got...)
	}
}

func post(t *testing.T, h http.Handler, path, contentType, body string, header ...string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)

	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	return w
}

// parts is a message's text, HTML and attachments' names, read the way a mail client would.
func parts(t *testing.T, m *mail.Message) (text, html string, files []string) {
	t.Helper()

	var walk func(contentType string, body io.Reader)

	walk = func(contentType string, body io.Reader) {
		kind, params, _ := mime.ParseMediaType(contentType)

		if strings.HasPrefix(kind, "multipart/") {
			mr := multipart.NewReader(body, params["boundary"])

			for {
				p, err := mr.NextPart()
				if err != nil {
					return
				}

				if name := p.FileName(); name != "" {
					files = append(files, name)

					continue
				}

				walk(p.Header.Get("Content-Type"), p)
			}
		}

		b, _ := io.ReadAll(body)

		switch kind {
		case "text/plain":
			text = string(b)
		case "text/html":
			html = string(b)
		}
	}

	walk(m.Header.Get("Content-Type"), m.Body)

	return text, html, files
}

func TestResendSendsToSMTP(t *testing.T) {
	addr, got := smtpServer(t)
	s := &Server{SMTP: addr}
	h := s.Handler()

	body := `{"from": "Acme <no-reply@acme.test>", "to": "kari@acme.test", "bcc": ["audit@acme.test"], "subject": "Welcome, Kåri", "text": "Hi", "html": "<p>Hi</p>",
		"attachments": [{"filename": "terms.txt", "content": "` + base64.StdEncoding.EncodeToString([]byte("the terms")) + `"}, {"filename": "logo.png", "content": {"type": "Buffer", "data": [137, 80]}}]}`

	w := post(t, h, "/resend/emails", "application/json", body, "Authorization", "Bearer re_test")

	var answer struct{ ID string }

	_ = json.Unmarshal(w.Body.Bytes(), &answer)

	if w.Code != http.StatusOK || len(answer.ID) != 36 {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}

	msgs := got()

	if len(msgs) != 1 {
		t.Fatalf("SMTP got %d messages", len(msgs))
	}

	m := msgs[0]

	// Bcc goes on the envelope, not in the message.
	if m.from != "no-reply@acme.test" || strings.Join(m.to, ",") != "kari@acme.test,audit@acme.test" || m.msg.Header.Get("Bcc") != "" {
		t.Fatalf("envelope %s → %v, Bcc header %q", m.from, m.to, m.msg.Header.Get("Bcc"))
	}

	if subject, _ := new(mime.WordDecoder).DecodeHeader(m.msg.Header.Get("Subject")); subject != "Welcome, Kåri" {
		t.Fatalf("subject %q", subject)
	}

	text, html, files := parts(t, m.msg)

	if text != "Hi" || html != "<p>Hi</p>" || strings.Join(files, ",") != "terms.txt,logo.png" {
		t.Fatalf("text %q, html %q, files %v", text, html, files)
	}

	// The app may look the email up again.
	req := httptest.NewRequest(http.MethodGet, "/resend/emails/"+answer.ID, nil)
	lookup := httptest.NewRecorder()
	h.ServeHTTP(lookup, req)

	if !strings.Contains(lookup.Body.String(), `"last_event":"delivered"`) {
		t.Fatalf("lookup %s", lookup.Body)
	}
}

func TestResendRefusesAsResendDoes(t *testing.T) {
	s := &Server{}

	w := post(t, s.Handler(), "/resend/emails", "application/json", `{"from": "a@acme.test", "subject": "x", "text": "y"}`)

	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Missing `to` field.") {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}

	batch := post(t, s.Handler(), "/resend/emails/batch", "application/json", `[{"from": "a@acme.test", "to": "b@acme.test", "subject": "1", "text": "y"}, {"from": "a@acme.test", "to": "c@acme.test", "subject": "2", "text": "y"}]`)

	var answer struct{ Data []struct{ ID string } }

	_ = json.Unmarshal(batch.Body.Bytes(), &answer)

	if batch.Code != http.StatusOK || len(answer.Data) != 2 {
		t.Fatalf("batch answered %d %s", batch.Code, batch.Body)
	}
}

func TestSESSendsToSMTP(t *testing.T) {
	addr, got := smtpServer(t)
	s := &Server{SMTP: addr}
	h := s.AWSHandler()

	// SES's first API: the query protocol.
	form := url.Values{
		"Action":                           {"SendEmail"},
		"Source":                           {"no-reply@acme.test"},
		"Destination.ToAddresses.member.1": {"kari@acme.test"},
		"Destination.ToAddresses.member.2": {"ola@acme.test"},
		"Message.Subject.Data":             {"Your receipt"},
		"Message.Body.Text.Data":           {"Thanks"},
	}

	v1 := post(t, h, "/", "application/x-www-form-urlencoded", form.Encode())

	if v1.Code != http.StatusOK || !strings.Contains(v1.Body.String(), "<SendEmailResult><MessageId>") {
		t.Fatalf("SendEmail answered %d %s", v1.Code, v1.Body)
	}

	raw := "From: shop@acme.test\r\nTo: kari@acme.test\r\nSubject: Raw\r\n\r\nraw body\r\n"
	rawForm := url.Values{"Action": {"SendRawEmail"}, "RawMessage.Data": {base64.StdEncoding.EncodeToString([]byte(raw))}}

	if w := post(t, h, "/", "application/x-www-form-urlencoded", rawForm.Encode()); !strings.Contains(w.Body.String(), "<SendRawEmailResult>") {
		t.Fatalf("SendRawEmail answered %d %s", w.Code, w.Body)
	}

	// SES's v2 API: JSON.
	v2 := post(t, h, "/v2/email/outbound-emails", "application/json",
		`{"FromEmailAddress": "no-reply@acme.test", "Destination": {"ToAddresses": ["kari@acme.test"]}, "Content": {"Simple": {"Subject": {"Data": "v2"}, "Body": {"Html": {"Data": "<b>v2</b>"}}}}}`)

	if v2.Code != http.StatusOK || !strings.Contains(v2.Body.String(), `"MessageId":"010001`) {
		t.Fatalf("v2 answered %d %s", v2.Code, v2.Body)
	}

	msgs := got()

	if len(msgs) != 3 {
		t.Fatalf("SMTP got %d messages", len(msgs))
	}

	if strings.Join(msgs[0].to, ",") != "kari@acme.test,ola@acme.test" || msgs[0].msg.Header.Get("Subject") != "Your receipt" {
		t.Fatalf("SendEmail went as %v %q", msgs[0].to, msgs[0].msg.Header.Get("Subject"))
	}

	// A raw message goes as it was, to its own recipients.
	if msgs[1].raw != raw || msgs[1].from != "shop@acme.test" || strings.Join(msgs[1].to, ",") != "kari@acme.test" {
		t.Fatalf("SendRawEmail went as %s → %v:\n%s", msgs[1].from, msgs[1].to, msgs[1].raw)
	}

	// SMTP ends the data with a line break of its own.
	if _, html, _ := parts(t, msgs[2].msg); strings.TrimRight(html, "\r\n") != "<b>v2</b>" {
		t.Fatalf("v2's HTML %q", html)
	}
}

func TestSNSKeepsTextMessages(t *testing.T) {
	s := &Server{}
	h := s.AWSHandler()

	query := post(t, h, "/", "application/x-www-form-urlencoded", url.Values{"Action": {"Publish"}, "PhoneNumber": {"+4791234567"}, "Message": {"Your code is 123456"}}.Encode())

	if !strings.Contains(query.Body.String(), "<PublishResult><MessageId>") {
		t.Fatalf("Publish answered %d %s", query.Code, query.Body)
	}

	jsonProtocol := post(t, h, "/", "application/x-amz-json-1.0", `{"PhoneNumber": "+4799999999", "Message": "Hi"}`, "X-Amz-Target", "AmazonSNS.Publish")

	if !strings.Contains(jsonProtocol.Body.String(), `"MessageId"`) {
		t.Fatalf("JSON Publish answered %d %s", jsonProtocol.Code, jsonProtocol.Body)
	}

	// A topic isn't something the mock delivers: refused, so nothing seems sent that wasn't.
	if w := post(t, h, "/", "application/x-www-form-urlencoded", url.Values{"Action": {"Publish"}, "TopicArn": {"arn:aws:sns:eu-north-1:1:orders"}, "Message": {"x"}}.Encode()); w.Code != http.StatusBadRequest {
		t.Fatalf("topic Publish answered %d", w.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/texts", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	var answer struct{ Texts []Text }

	_ = json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&answer)

	if len(answer.Texts) != 2 || answer.Texts[0].To != "+4799999999" || answer.Texts[1].Body != "Your code is 123456" {
		t.Fatalf("texts %+v", answer.Texts)
	}
}
