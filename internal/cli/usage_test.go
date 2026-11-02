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

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeUsage is a preview with two services sampled: webapp over a few
// minutes, postgres once.
type fakeUsage struct {
	apiv1connect.UnimplementedPreviewServiceHandler

	asked []*apiv1.GetServiceUsageRequest
	none  bool
}

func (f *fakeUsage) GetServiceUsage(_ context.Context, req *connect.Request[apiv1.GetServiceUsageRequest]) (*connect.Response[apiv1.GetServiceUsageResponse], error) {
	f.asked = append(f.asked, req.Msg)

	if f.none {
		return connect.NewResponse(&apiv1.GetServiceUsageResponse{}), nil
	}

	now := time.Now()
	at := func(minutesAgo int) *timestamppb.Timestamp {
		return timestamppb.New(now.Add(-time.Duration(minutesAgo) * time.Minute))
	}
	mib := int64(1 << 20)

	return connect.NewResponse(&apiv1.GetServiceUsageResponse{Services: []*apiv1.ServiceUsage{
		{
			Service:          "webapp",
			Points:           []*apiv1.UsagePoint{{Time: at(3), CpuPercent: 2, MemoryBytes: 300 * mib}, {Time: at(2), CpuPercent: 4, MemoryBytes: 400 * mib}, {Time: at(1), CpuPercent: 6, MemoryBytes: 366 * mib}},
			SampledAt:        at(1),
			CpuPercent:       6,
			MemoryBytes:      366 * mib,
			MemoryLimitBytes: 16 << 30,
			Restarts:         1,
			StartedAt:        at(64),
			Image:            "stagd-build/webapp:stagd-preview",
		},
		{Service: "stagd-postgres", SampledAt: at(1), CpuPercent: 0.5, MemoryBytes: 82 * mib, MemoryLimitBytes: 16 << 30, Image: "postgres:18.6-trixie"},
	}}), nil
}

func usageServer(t *testing.T, f *fakeUsage) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)

	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)

	t.Cleanup(srv.Close)

	return srv
}

func TestPreviewUsageShowsEachService(t *testing.T) {
	f := &fakeUsage{}

	out, err := run(t, usageServer(t, f), "--token", "tok-1", "preview", "usage", "p-1", "--window", "6h")
	if err != nil {
		t.Fatal(err)
	}

	// Now, the averages and peaks over the window, restarts and how long it has run.
	for _, want := range []string{"webapp", "6.0%", "366 MiB (2%)", "4.0%", "400 MiB", "1h04m", "stagd-postgres", "82.0 MiB (1%)", "1m ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}

	if got := f.asked[0].GetWindow().AsDuration(); got != 6*time.Hour {
		t.Errorf("window: %s", got)
	}
}

func TestPreviewUsageSaysWhenNothingIsSampled(t *testing.T) {
	out, err := run(t, usageServer(t, &fakeUsage{none: true}), "--token", "tok-1", "preview", "usage", "p-1")
	if err != nil {
		t.Fatal(err)
	}

	if out != "" {
		t.Errorf("output: %q", out)
	}
}

func TestPreviewUsageRefusesAWindowItCantAsk(t *testing.T) {
	_, err := run(t, usageServer(t, &fakeUsage{}), "--token", "tok-1", "preview", "usage", "p-1", "--window", "48h")

	if err == nil || !strings.Contains(err.Error(), "from 1m to 24h") {
		t.Errorf("err: %v", err)
	}
}

func TestSizeOfAndShortSpan(t *testing.T) {
	for b, want := range map[int64]string{512: "512 B", 82 << 20: "82.0 MiB", 366 << 20: "366 MiB", 1536 << 20: "1.5 GiB"} {
		if got := sizeOf(b); got != want {
			t.Errorf("sizeOf(%d) = %q, want %q", b, got, want)
		}
	}

	for d, want := range map[time.Duration]string{45 * time.Second: "45s", 12 * time.Minute: "12m", 185 * time.Minute: "3h05m", 52 * time.Hour: "2d4h"} {
		if got := shortSpan(d); got != want {
			t.Errorf("shortSpan(%s) = %q, want %q", d, got, want)
		}
	}
}
