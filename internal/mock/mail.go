package mock

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// Email is a message a vendor's API was asked to send, as the mock hands it on.
type Email struct {
	// Vendor names the API it came through, e.g. "resend".
	Vendor string

	From    string
	To      []string
	Cc      []string
	Bcc     []string
	ReplyTo []string
	Subject string
	Text    string
	HTML    string
	Headers map[string]string

	Attachments []Attachment

	// Raw is a whole message as sent (SES's raw email): sent as it is, the rest unused but for the envelope.
	Raw []byte
}

// Attachment is a file attached to an Email.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// newID is a random id in the form vendors use: hex, 32 characters.
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// uuid is a random version 4 UUID, as Resend's ids are.
func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	h := hex.EncodeToString(b)

	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// addressOf is the bare address in "Name <a@b>", or the text as it is when it isn't one.
func addressOf(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}

	return strings.TrimSpace(s)
}

// recipients is the envelope's: To, Cc and Bcc, bare addresses.
func (e Email) recipients() []string {
	var out []string

	for _, list := range [][]string{e.To, e.Cc, e.Bcc} {
		for _, a := range list {
			if a = addressOf(a); a != "" {
				out = append(out, a)
			}
		}
	}

	return out
}

// header is a header's value, encoded when it isn't plain ASCII.
func header(s string) string {
	return mime.QEncoding.Encode("utf-8", s)
}

// Message is the Email as RFC 5322: text and HTML as alternatives, attachments beside them.
func (e Email) Message(id string) []byte {
	if len(e.Raw) > 0 {
		return e.Raw
	}

	var b bytes.Buffer

	h := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\r\n", name, value)
		}
	}

	h("From", e.From)
	h("To", strings.Join(e.To, ", "))
	h("Cc", strings.Join(e.Cc, ", "))
	h("Reply-To", strings.Join(e.ReplyTo, ", "))
	h("Subject", header(e.Subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+id+"@stagd-mock>")
	h("X-Stagd-Mock", e.Vendor)

	for name, value := range e.Headers {
		h(textproto.CanonicalMIMEHeaderKey(name), header(value))
	}

	h("MIME-Version", "1.0")

	bodyHeader, body := e.body()

	if len(e.Attachments) == 0 {
		for name, values := range bodyHeader {
			h(name, values[0])
		}

		b.WriteString("\r\n")
		b.Write(body)

		return b.Bytes()
	}

	w := multipart.NewWriter(&b)

	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", w.Boundary())

	part, _ := w.CreatePart(bodyHeader)
	_, _ = part.Write(body)

	for _, a := range e.Attachments {
		kind := a.ContentType

		if kind == "" {
			kind = mime.TypeByExtension(a.Filename[strings.LastIndex(a.Filename, ".")+1:])
		}

		if kind == "" {
			kind = "application/octet-stream"
		}

		ph := textproto.MIMEHeader{}
		ph.Set("Content-Type", kind)
		ph.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename}))
		ph.Set("Content-Transfer-Encoding", "base64")

		part, _ := w.CreatePart(ph)
		enc := base64.NewEncoder(base64.StdEncoding, &lineBreaker{w: part})
		_, _ = enc.Write(a.Content)
		_ = enc.Close()
	}

	_ = w.Close()

	return b.Bytes()
}

// body is the message's text and HTML: its Content-Type headers, and what follows them.
func (e Email) body() (textproto.MIMEHeader, []byte) {
	var b bytes.Buffer

	header := textproto.MIMEHeader{}

	quoted := func(content string) {
		qp := quotedprintable.NewWriter(&b)
		_, _ = qp.Write([]byte(content))
		_ = qp.Close()
	}

	switch {
	case e.Text != "" && e.HTML != "":
		w := multipart.NewWriter(&b)

		header.Set("Content-Type", "multipart/alternative; boundary="+w.Boundary())

		for _, alt := range []struct{ kind, content string }{{"text/plain", e.Text}, {"text/html", e.HTML}} {
			ph := textproto.MIMEHeader{}
			ph.Set("Content-Type", alt.kind+"; charset=utf-8")
			ph.Set("Content-Transfer-Encoding", "quoted-printable")

			part, _ := w.CreatePart(ph)
			qp := quotedprintable.NewWriter(part)
			_, _ = qp.Write([]byte(alt.content))
			_ = qp.Close()
		}

		_ = w.Close()

	case e.HTML != "":
		header.Set("Content-Type", "text/html; charset=utf-8")
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		quoted(e.HTML)

	default:
		header.Set("Content-Type", "text/plain; charset=utf-8")
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		quoted(e.Text)
	}

	return header, b.Bytes()
}

// lineBreaker breaks base64 into lines of 76, as MIME wants.
type lineBreaker struct {
	w    interface{ Write([]byte) (int, error) }
	used int
}

func (l *lineBreaker) Write(p []byte) (int, error) {
	n := len(p)

	for len(p) > 0 {
		room := 76 - l.used
		chunk := min(room, len(p))

		if _, err := l.w.Write(p[:chunk]); err != nil {
			return 0, err
		}

		l.used += chunk
		p = p[chunk:]

		if l.used == 76 {
			if _, err := l.w.Write([]byte("\r\n")); err != nil {
				return 0, err
			}

			l.used = 0
		}
	}

	return n, nil
}

// deliver hands an Email to the SMTP server (the preview's Mailpit).
func deliver(addr string, e Email, id string) error {
	to := e.recipients()

	if len(to) == 0 {
		return fmt.Errorf("no recipients")
	}

	from := addressOf(e.From)

	if from == "" && len(e.Raw) > 0 {
		from = "stagd-mock@localhost"
	}

	return smtp.SendMail(addr, nil, from, to, e.Message(id))
}

// rawHeader is a header of a raw message, decoded; empty when it hasn't got one.
func rawHeader(raw []byte, name string) string {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}

	value := m.Header.Get(name)

	if decoded, err := new(mime.WordDecoder).DecodeHeader(value); err == nil {
		return decoded
	}

	return value
}

// rawRecipients is a raw message's own To, Cc and Bcc.
func rawRecipients(raw []byte) []string {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}

	var out []string

	for _, name := range []string{"To", "Cc", "Bcc"} {
		list, err := m.Header.AddressList(name)
		if err != nil {
			continue
		}

		for _, a := range list {
			out = append(out, a.Address)
		}
	}

	return out
}
