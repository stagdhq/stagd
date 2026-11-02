package mock

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Amazon's APIs, at the root of the mock's AWS port, as the AWS SDKs call
// them with AWS_ENDPOINT_URL_SES, _SESV2 and _SNS set: SES's v2 API
// (JSON), SES's first (the query protocol, XML answers) and SNS's Publish
// (the query protocol, or the JSON one newer SDKs may use). Requests are
// signed (SigV4); the mock doesn't check the signature.

// AWSHandler answers SES and SNS.
func (s *Server) AWSHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})

	mux.HandleFunc("POST /v2/email/outbound-emails", s.sesV2)

	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		if target := r.Header.Get("X-Amz-Target"); target != "" {
			s.awsJSON(w, r, target)

			return
		}

		if err := r.ParseForm(); err != nil {
			awsError(w, "MalformedQueryString", err.Error())

			return
		}

		switch action := r.PostForm.Get("Action"); action {
		case "SendEmail":
			s.sesSendEmail(w, r.PostForm)
		case "SendRawEmail":
			s.sesSendRawEmail(w, r.PostForm)
		case "Publish":
			s.snsPublish(w, r.PostForm)
		default:
			awsError(w, "InvalidAction", fmt.Sprintf("stagd mock answers SES's SendEmail and SendRawEmail and SNS's Publish, not %q", action))
		}
	})

	return mux
}

// awsError answers as the query APIs do when they refuse.
func awsError(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusBadRequest)

	_, _ = fmt.Fprintf(w, `<ErrorResponse><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`, code, xmlText(message), uuid())
}

func xmlText(s string) string {
	var b strings.Builder

	_ = xml.EscapeText(&b, []byte(s))

	return b.String()
}

// members reads a query list: Destination.ToAddresses.member.1, .member.2, …
func members(form url.Values, prefix string) []string {
	type item struct {
		n     int
		value string
	}

	var items []item

	for key, values := range form {
		var n int

		if _, err := fmt.Sscanf(strings.TrimPrefix(key, prefix+".member."), "%d", &n); err == nil && strings.HasPrefix(key, prefix+".member.") {
			items = append(items, item{n, values[0]})
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].n < items[j].n })

	out := make([]string, 0, len(items))

	for _, it := range items {
		out = append(out, it.value)
	}

	return out
}

func (s *Server) sesSendEmail(w http.ResponseWriter, form url.Values) {
	e := Email{
		Vendor:  "ses",
		From:    form.Get("Source"),
		To:      members(form, "Destination.ToAddresses"),
		Cc:      members(form, "Destination.CcAddresses"),
		Bcc:     members(form, "Destination.BccAddresses"),
		ReplyTo: members(form, "ReplyToAddresses"),
		Subject: form.Get("Message.Subject.Data"),
		Text:    form.Get("Message.Body.Text.Data"),
		HTML:    form.Get("Message.Body.Html.Data"),
	}

	if e.From == "" || len(e.recipients()) == 0 {
		awsError(w, "InvalidParameterValue", "Missing final '@domain'")

		return
	}

	sesAnswer(w, "SendEmail", s.send(e))
}

func (s *Server) sesSendRawEmail(w http.ResponseWriter, form url.Values) {
	raw, err := base64.StdEncoding.DecodeString(form.Get("RawMessage.Data"))
	if err != nil || len(raw) == 0 {
		awsError(w, "InvalidParameterValue", "RawMessage.Data should be a base64 message")

		return
	}

	e := Email{Vendor: "ses", From: form.Get("Source"), To: members(form, "Destinations"), Raw: raw}

	// Without Destinations, SES sends to the message's own To, Cc and Bcc.
	if len(e.To) == 0 {
		e.To = rawRecipients(raw)
	}

	if e.From == "" {
		e.From = rawHeader(raw, "From")
	}

	e.Subject = rawHeader(raw, "Subject")

	sesAnswer(w, "SendRawEmail", s.send(e))
}

// sesAnswer is SES's first API's answer: XML with the message id.
func sesAnswer(w http.ResponseWriter, action, id string) {
	w.Header().Set("Content-Type", "text/xml")

	_, _ = fmt.Fprintf(w, `<%[1]sResponse xmlns="http://ses.amazonaws.com/doc/2010-12-01/"><%[1]sResult><MessageId>%[2]s</MessageId></%[1]sResult><ResponseMetadata><RequestId>%[3]s</RequestId></ResponseMetadata></%[1]sResponse>`, action, sesMessageID(id), uuid())
}

// sesMessageID looks like SES's: 0100018…-…-000000.
func sesMessageID(id string) string {
	return "010001" + strings.ReplaceAll(id, "-", "")[:26] + "-" + id[24:] + "-000000"
}

type sesV2Request struct {
	FromEmailAddress string `json:"FromEmailAddress"`
	Destination      struct {
		ToAddresses  []string `json:"ToAddresses"`
		CcAddresses  []string `json:"CcAddresses"`
		BccAddresses []string `json:"BccAddresses"`
	} `json:"Destination"`
	ReplyToAddresses []string `json:"ReplyToAddresses"`
	Content          struct {
		Simple *struct {
			Subject struct{ Data string } `json:"Subject"`
			Body    struct {
				Text *struct{ Data string } `json:"Text"`
				HTML *struct{ Data string } `json:"Html"`
			} `json:"Body"`
		} `json:"Simple"`
		Raw *struct {
			Data string `json:"Data"`
		} `json:"Raw"`
		Template *struct {
			TemplateName string `json:"TemplateName"`
			TemplateData string `json:"TemplateData"`
		} `json:"Template"`
	} `json:"Content"`
}

// sesV2 answers SendEmail in SES's v2 API: simple, raw or a template, which the mock shows rather than renders.
func (s *Server) sesV2(w http.ResponseWriter, r *http.Request) {
	var req sesV2Request

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		awsJSONError(w, "BadRequestException", err.Error())

		return
	}

	e := Email{
		Vendor:  "ses",
		From:    req.FromEmailAddress,
		To:      req.Destination.ToAddresses,
		Cc:      req.Destination.CcAddresses,
		Bcc:     req.Destination.BccAddresses,
		ReplyTo: req.ReplyToAddresses,
	}

	switch c := req.Content; {
	case c.Simple != nil:
		e.Subject = c.Simple.Subject.Data

		if c.Simple.Body.Text != nil {
			e.Text = c.Simple.Body.Text.Data
		}

		if c.Simple.Body.HTML != nil {
			e.HTML = c.Simple.Body.HTML.Data
		}

	case c.Raw != nil:
		raw, err := base64.StdEncoding.DecodeString(c.Raw.Data)
		if err != nil {
			raw = []byte(c.Raw.Data)
		}

		e.Raw = raw
		e.Subject = rawHeader(raw, "Subject")

		if e.From == "" {
			e.From = rawHeader(raw, "From")
		}

		if len(e.recipients()) == 0 {
			e.To = rawRecipients(raw)
		}

	case c.Template != nil:
		e.Subject = "SES template " + c.Template.TemplateName
		e.Text = "The app sent SES's template " + c.Template.TemplateName + ", which stagd mock doesn't have, with this data:\n\n" + c.Template.TemplateData

	default:
		awsJSONError(w, "BadRequestException", "Content should be Simple, Raw or Template")

		return
	}

	if len(e.recipients()) == 0 {
		awsJSONError(w, "BadRequestException", "Destination has no addresses")

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"MessageId": sesMessageID(s.send(e))})
}

func awsJSONError(w http.ResponseWriter, code, message string) {
	w.Header().Set("X-Amzn-ErrorType", code)
	writeJSON(w, http.StatusBadRequest, map[string]string{"__type": code, "message": message})
}

// snsPublish keeps an SMS (a Publish to a phone number); a Publish to a topic isn't something the mock delivers.
func (s *Server) snsPublish(w http.ResponseWriter, form url.Values) {
	phone, message := form.Get("PhoneNumber"), form.Get("Message")

	if phone == "" {
		awsError(w, "InvalidParameter", "stagd mock catches text messages (PhoneNumber), not publishes to topics")

		return
	}

	id := s.text(Text{Vendor: "sns", To: phone, Body: message})

	w.Header().Set("Content-Type", "text/xml")

	_, _ = fmt.Fprintf(w, `<PublishResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/"><PublishResult><MessageId>%s</MessageId></PublishResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></PublishResponse>`, uuidOf(id), uuid())
}

// awsJSON answers the JSON protocol (X-Amz-Target): SNS's Publish.
func (s *Server) awsJSON(w http.ResponseWriter, r *http.Request, target string) {
	if !strings.HasSuffix(target, ".Publish") {
		awsJSONError(w, "InvalidAction", "stagd mock answers SNS's Publish, not "+target)

		return
	}

	var req struct {
		PhoneNumber string `json:"PhoneNumber"`
		Message     string `json:"Message"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PhoneNumber == "" {
		awsJSONError(w, "InvalidParameter", "stagd mock catches text messages (PhoneNumber), not publishes to topics")

		return
	}

	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	writeJSON(w, http.StatusOK, map[string]string{"MessageId": uuidOf(s.text(Text{Vendor: "sns", To: req.PhoneNumber, Body: req.Message}))})
}

// uuidOf shows a 32-character hex id as a UUID.
func uuidOf(id string) string {
	if len(id) != 32 {
		return id
	}

	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
}
