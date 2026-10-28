package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for the bridge's writers and the test's reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buf.Write(p)
}

func (s *syncBuffer) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return strings.Split(strings.TrimSpace(s.buf.String()), "\n")
}

func bridgeTo(server *httptest.Server, out io.Writer) *mcpBridge {
	return &mcpBridge{endpoint: server.URL + mcpPath, token: "tok", userAgent: "stagd-cli/test", http: server.Client(), out: out, log: io.Discard}
}

func TestMCPBridgeForwardsEachMessageWithTheToken(t *testing.T) {
	var seen []string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("got %s %s with %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}

		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}

		_ = json.NewDecoder(r.Body).Decode(&m)

		mu.Lock()
		seen = append(seen, m.Method)
		mu.Unlock()

		if len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Pretty-printed on purpose: stdio wants one message per line.
		_, _ = w.Write([]byte("{\n  \"jsonrpc\": \"2.0\",\n  \"id\": " + string(m.ID) + ",\n  \"result\": {\"method\": \"" + m.Method + "\"}\n}"))
	}))
	defer server.Close()

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		``,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
	}, "\n") + "\n"

	out := &syncBuffer{}

	if err := bridgeTo(server, out).run(context.Background(), strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}

	lines := out.lines()
	sort.Strings(lines)

	want := []string{
		`{"jsonrpc":"2.0","id":"two","result":{"method":"tools/list"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"method":"initialize"}}`,
	}

	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	if len(seen) != 3 {
		t.Fatalf("forwarded %v, want all three messages", seen)
	}
}

func TestMCPBridgeSaysWhenTheSignInIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	out := &syncBuffer{}

	if err := bridgeTo(server, out).run(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)); err != nil {
		t.Fatal(err)
	}

	var got struct {
		ID    int `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(out.lines()[0]), &got); err != nil {
		t.Fatal(err)
	}

	if got.ID != 7 || got.Error.Code != -32001 || !strings.Contains(got.Error.Message, "stagd login") {
		t.Fatalf("got %+v", got)
	}
}

func TestMCPBridgeDoesntHoldOneCallBehindAnother(t *testing.T) {
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// The slow one (a wait_for_preview) waits until the quick one has been answered.
		if bytes.Contains(body, []byte(`"slow"`)) {
			<-release
		}

		var m struct {
			ID json.RawMessage `json:"id"`
		}

		_ = json.Unmarshal(body, &m)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":{}}`))
	}))
	defer server.Close()

	reader, writer := io.Pipe()
	out := &syncBuffer{}
	done := make(chan error, 1)

	go func() { done <- bridgeTo(server, out).run(context.Background(), reader) }()

	_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":"slow","method":"tools/call"}` + "\n" + `{"jsonrpc":"2.0","id":"quick","method":"ping"}` + "\n"))

	deadline := time.Now().Add(5 * time.Second)

	for !strings.Contains(strings.Join(out.lines(), ""), `"quick"`) {
		if time.Now().After(deadline) {
			t.Fatal("the quick call waited behind the slow one")
		}

		time.Sleep(10 * time.Millisecond)
	}

	close(release)
	_ = writer.Close()

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if got := len(out.lines()); got != 2 {
		t.Fatalf("got %d answers, want 2", got)
	}
}
