package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

var tracesAt = time.Date(2026, 11, 4, 13, 0, 0, 0, time.UTC)

const checkoutTrace = "4bf92f3577b34da6a3ce929d0e0e4736"

func span(id, parent, name, service string, at, took time.Duration) *apiv1.Span {
	return &apiv1.Span{
		SpanId: id, ParentSpanId: parent, Name: name, Service: service,
		StartTime: timestamppb.New(tracesAt.Add(at)), Duration: durationpb.New(took),
	}
}

// A checkout: web calls api, which asks the database twice and fails on the payment.
func checkoutSpans() []*apiv1.Span {
	pay := span("d", "b", "POST payments", "api", 60*time.Millisecond, 30*time.Millisecond)
	pay.Error = true
	pay.StatusMessage = "card declined"
	pay.Attributes = []*apiv1.LogField{{Key: "http.status_code", Value: "502"}}

	// Out of order on purpose, as spans can arrive.
	return []*apiv1.Span{
		pay,
		span("c2", "b", "SELECT orders", "api", 40*time.Millisecond, 5*time.Millisecond),
		span("a", "", "POST /checkout", "web", 0, 100*time.Millisecond),
		span("c1", "b", "SELECT carts", "api", 20*time.Millisecond, 8*time.Millisecond),
		span("b", "a", "POST /api/orders", "api", 10*time.Millisecond, 85*time.Millisecond),
	}
}

type fakeTraces struct {
	apiv1connect.UnimplementedPreviewServiceHandler
	asked *apiv1.ListTracesRequest
	none  bool
}

func (f *fakeTraces) ListTraces(_ context.Context, req *connect.Request[apiv1.ListTracesRequest]) (*connect.Response[apiv1.ListTracesResponse], error) {
	f.asked = req.Msg

	if f.none {
		return connect.NewResponse(&apiv1.ListTracesResponse{Services: []string{"web", "api"}}), nil
	}

	return connect.NewResponse(&apiv1.ListTracesResponse{
		Services: []string{"api", "web"},
		Traces: []*apiv1.TraceSummary{{
			TraceId: checkoutTrace, Name: "/checkout", Service: "web", HttpMethod: "POST", HttpStatusCode: 500,
			StartTime: timestamppb.New(tracesAt), Duration: durationpb.New(100 * time.Millisecond), SpanCount: 5, Error: true,
		}},
	}), nil
}

func (f *fakeTraces) GetTrace(_ context.Context, req *connect.Request[apiv1.GetTraceRequest]) (*connect.Response[apiv1.GetTraceResponse], error) {
	return connect.NewResponse(&apiv1.GetTraceResponse{TraceId: req.Msg.GetTraceId(), Spans: checkoutSpans()}), nil
}

func tracesServer(t *testing.T, f *fakeTraces) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestPreviewTracesLists(t *testing.T) {
	f := &fakeTraces{}
	srv := tracesServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "preview", "traces", "p-1", "--errors", "--service", "web", "--slower-than", "250ms", "--since", "1h")
	if err != nil {
		t.Fatalf("preview traces: %v\n%s", err, out)
	}

	for _, want := range []string{"POST /checkout 500", "100ms", "error", checkoutTrace} {
		if !strings.Contains(out, want) {
			t.Errorf("list misses %q:\n%s", want, out)
		}
	}

	a := f.asked
	if !a.GetErrorsOnly() || a.GetService() != "web" || a.GetMinDuration().AsDuration() != 250*time.Millisecond || a.GetLimit() != 50 || a.GetSince() == nil {
		t.Errorf("asked %v", a)
	}
}

func TestPreviewTracesSaysWhyNone(t *testing.T) {
	srv := tracesServer(t, &fakeTraces{none: true})

	var errOut strings.Builder
	cmd := New("test")
	cmd.Writer = &strings.Builder{}
	cmd.ErrWriter = &errOut

	if err := cmd.Run(context.Background(), []string{"stagd", "--api-url", srv.URL, "--token", "tok-1", "preview", "traces", "p-1", "--errors"}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(errOut.String(), "No traces like that") || !strings.Contains(errOut.String(), "web, api") {
		t.Errorf("says %q", errOut.String())
	}
}

func TestPreviewTracesRefusesABadTraceID(t *testing.T) {
	srv := tracesServer(t, &fakeTraces{})

	if _, err := run(t, srv, "--token", "tok-1", "preview", "traces", "p-1", "nope"); err == nil {
		t.Error("a trace id that isn't 32 hex digits should refuse")
	}
}

func TestPreviewTraceWaterfall(t *testing.T) {
	srv := tracesServer(t, &fakeTraces{})

	out, err := run(t, srv, "--token", "tok-1", "preview", "traces", "p-1", checkoutTrace, "--attributes")
	if err != nil {
		t.Fatalf("waterfall: %v\n%s", err, out)
	}

	lines := strings.Split(out, "\n")

	// Each span under its caller, siblings by when they started.
	var order []string

	for _, l := range lines {
		for _, name := range []string{"POST /checkout", "POST /api/orders", "SELECT carts", "SELECT orders", "POST payments"} {
			if strings.HasPrefix(strings.TrimSpace(l), name+" ") {
				order = append(order, strings.Repeat("  ", (len(l)-len(strings.TrimLeft(l, " ")))/2)+name)
			}
		}
	}

	want := []string{"POST /checkout", "  POST /api/orders", "    SELECT carts", "    SELECT orders", "    POST payments"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Errorf("order:\n%s\nwant:\n%s\n\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"), out)
	}

	for _, want := range []string{"5 spans", "100ms", "+60ms", "error", "card declined", "http.status_code: 502"} {
		if !strings.Contains(out, want) {
			t.Errorf("waterfall misses %q:\n%s", want, out)
		}
	}

	// Not a terminal: no bars, no color.
	if strings.Contains(out, "█") || strings.Contains(out, "\x1b[") {
		t.Errorf("piped waterfall has bars or color:\n%s", out)
	}
}

func TestPlaceSpansStartsATreeForACallerOutsideTheTrace(t *testing.T) {
	placed := placeSpans([]*apiv1.Span{
		span("x", "from-another-service", "consume", "worker", 5*time.Millisecond, time.Millisecond),
		span("a", "", "root", "web", 0, 10*time.Millisecond),
	})

	if len(placed) != 2 || placed[0].span.GetSpanId() != "a" || placed[1].depth != 0 {
		t.Errorf("placed %v", placed)
	}
}

func TestBar(t *testing.T) {
	for _, c := range []struct {
		at, took time.Duration
		want     string
	}{
		{0, 100, "██████████"},
		{50, 50, "     █████"},
		{90, 0, "         █"},
		{0, 1, "█         "},
	} {
		if got := bar(10, c.at, c.took, 100); got != c.want {
			t.Errorf("bar(at %d, took %d) = %q, want %q", c.at, c.took, got, c.want)
		}
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		840 * time.Microsecond:  "840µs",
		2500 * time.Microsecond: "2.5ms",
		120 * time.Millisecond:  "120ms",
		1400 * time.Millisecond: "1.4s",
		125 * time.Second:       "2m5s",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
