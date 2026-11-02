package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

var logsAt = time.Date(2026, 11, 2, 13, 11, 7, 684e6, time.UTC)

// fakeLogs is a preview whose Postgres couldn't download its seed, answering
// as Stagd does: the latest lines at or after since.
type fakeLogs struct {
	apiv1connect.UnimplementedPreviewServiceHandler

	mu     sync.Mutex
	asked  []*apiv1.ListLogsRequest
	lines  []*apiv1.LogLine // written so far: seedLines when nil
	more   []*apiv1.LogLine // written after the first ask
	builds []*apiv1.BuildLog
}

func logLine(s int, service string, level apiv1.LogLevel, text string) *apiv1.LogLine {
	return &apiv1.LogLine{Time: timestamppb.New(logsAt.Add(time.Duration(s) * time.Second)), Service: service, Level: level, Text: text}
}

var seedLines = []*apiv1.LogLine{
	logLine(0, "stagd-postgres", apiv1.LogLevel_LOG_LEVEL_INFO, "stagd seed: downloading"),
	logLine(1, "stagd-postgres", apiv1.LogLevel_LOG_LEVEL_ERROR, "stagd seed: the server answered nothing"),
	logLine(2, "webapp", apiv1.LogLevel_LOG_LEVEL_UNSPECIFIED, "listening on :3000"),
}

func (f *fakeLogs) ListLogs(_ context.Context, req *connect.Request[apiv1.ListLogsRequest]) (*connect.Response[apiv1.ListLogsResponse], error) {
	if req.Msg.GetPreviewId() != "p-1" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, req.Msg)

	if f.lines == nil {
		f.lines = seedLines
	}

	// After the first ask, what's new has been written.
	if len(f.asked) > 1 {
		f.lines = append(f.lines, f.more...)
		f.more = nil
	}

	// At or after since, the latest as many as asked for.
	var lines []*apiv1.LogLine

	for _, l := range f.lines {
		if req.Msg.GetSince() == nil || !l.GetTime().AsTime().Before(req.Msg.GetSince().AsTime()) {
			lines = append(lines, l)
		}
	}

	lines = lines[max(0, len(lines)-int(req.Msg.GetLimit())):]

	var picked []*apiv1.LogLine

	for _, l := range lines {
		if len(req.Msg.GetServices()) == 0 || req.Msg.GetServices()[0] == l.GetService() {
			picked = append(picked, l)
		}
	}

	return connect.NewResponse(&apiv1.ListLogsResponse{Lines: picked}), nil
}

func (f *fakeLogs) ListBuildLogs(_ context.Context, _ *connect.Request[apiv1.ListBuildLogsRequest]) (*connect.Response[apiv1.ListBuildLogsResponse], error) {
	return connect.NewResponse(&apiv1.ListBuildLogsResponse{Builds: f.builds}), nil
}

func logsServer(t *testing.T, f *fakeLogs) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewPreviewServiceHandler(f)

	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)

	t.Cleanup(srv.Close)

	return srv
}

func TestPreviewLogsPrintsLinesToPaste(t *testing.T) {
	out, err := run(t, logsServer(t, &fakeLogs{}), "--token", "tok-1", "preview", "logs", "p-1")
	if err != nil {
		t.Fatal(err)
	}

	want := "2026-11-02T13:11:07.684Z stagd-postgres INFO stagd seed: downloading\n" +
		"2026-11-02T13:11:08.684Z stagd-postgres ERROR stagd seed: the server answered nothing\n" +
		"2026-11-02T13:11:09.684Z webapp listening on :3000\n"

	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
}

func TestPreviewLogsAsksForServicesAndSince(t *testing.T) {
	f := &fakeLogs{}

	out, err := run(t, logsServer(t, f), "--token", "tok-1", "preview", "logs", "p-1", "-s", "stagd-postgres", "--since", "10m", "--errors", "-n", "5")
	if err != nil {
		t.Fatal(err)
	}

	if out != "2026-11-02T13:11:08.684Z stagd-postgres ERROR stagd seed: the server answered nothing\n" {
		t.Errorf("output:\n%s", out)
	}

	got := f.asked[0]

	if strings.Join(got.GetServices(), ",") != "stagd-postgres" {
		t.Errorf("services: %v", got.GetServices())
	}

	// Errors are picked from more lines than are printed.
	if got.GetLimit() != 2000 {
		t.Errorf("limit: %d", got.GetLimit())
	}

	if ago := time.Since(got.GetSince().AsTime()); ago < 9*time.Minute || ago > 11*time.Minute {
		t.Errorf("since: %s ago", ago)
	}
}

func TestPreviewLogsRefusesASinceItCantRead(t *testing.T) {
	_, err := run(t, logsServer(t, &fakeLogs{}), "--token", "tok-1", "preview", "logs", "p-1", "--since", "yesterday")

	if err == nil || !strings.Contains(err.Error(), "give a duration") {
		t.Errorf("err: %v", err)
	}
}

func TestPreviewLogsFollowPrintsEachLineOnce(t *testing.T) {
	followEvery = 10 * time.Millisecond
	t.Cleanup(func() { followEvery = 2 * time.Second })

	f := &fakeLogs{more: []*apiv1.LogLine{logLine(3, "webapp", apiv1.LogLevel_LOG_LEVEL_INFO, "GET / 200")}}
	srv := logsServer(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var out strings.Builder
	cmd := New("test")
	cmd.Writer = &out
	cmd.ErrWriter = &strings.Builder{}

	if err := cmd.Run(ctx, []string{"stagd", "--api-url", srv.URL, "--token", "tok-1", "preview", "logs", "p-1", "-f"}); err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(out.String(), "listening on :3000"); n != 1 {
		t.Errorf("the last line printed %d times:\n%s", n, out.String())
	}

	if !strings.HasSuffix(out.String(), "webapp INFO GET / 200\n") {
		t.Errorf("output:\n%s", out.String())
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Following asks from the last line's time.
	if !f.asked[1].GetSince().AsTime().Equal(logsAt.Add(2 * time.Second)) {
		t.Errorf("followed since %s", f.asked[1].GetSince().AsTime())
	}
}

func TestPreviewLogsFollowLeavesOutWhatLinesCut(t *testing.T) {
	followEvery = 10 * time.Millisecond
	t.Cleanup(func() { followEvery = 2 * time.Second })

	// A stack trace, written at once: its lines share a time.
	trace := []*apiv1.LogLine{
		logLine(0, "webapp", apiv1.LogLevel_LOG_LEVEL_ERROR, "error: no such key"),
		logLine(0, "webapp", apiv1.LogLevel_LOG_LEVEL_ERROR, "    at unwrapKey"),
	}

	f := &fakeLogs{lines: trace, more: []*apiv1.LogLine{logLine(1, "webapp", apiv1.LogLevel_LOG_LEVEL_INFO, "GET / 200")}}
	srv := logsServer(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var out strings.Builder
	cmd := New("test")
	cmd.Writer = &out
	cmd.ErrWriter = &strings.Builder{}

	if err := cmd.Run(ctx, []string{"stagd", "--api-url", srv.URL, "--token", "tok-1", "preview", "logs", "p-1", "-n", "1", "-f"}); err != nil {
		t.Fatal(err)
	}

	want := "2026-11-02T13:11:07.684Z webapp ERROR     at unwrapKey\n2026-11-02T13:11:08.684Z webapp INFO GET / 200\n"

	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestPreviewLogsBuild(t *testing.T) {
	f := &fakeLogs{builds: []*apiv1.BuildLog{{Service: "webapp", Text: "#5 RUN npm ci\nERROR: exit code 1\n", Truncated: true}, {Service: "worker", Text: "done"}}}

	out, err := run(t, logsServer(t, f), "--token", "tok-1", "preview", "logs", "p-1", "--build", "-s", "webapp")
	if err != nil {
		t.Fatal(err)
	}

	if out != "==> webapp\n(the start left out)\n#5 RUN npm ci\nERROR: exit code 1\n" {
		t.Errorf("output:\n%s", out)
	}
}
