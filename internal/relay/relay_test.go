package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// connect starts a hub and a relay that dials it, and waits for the link.
func connect(t *testing.T, r *Relay) (*Hub, *httptest.Server) {
	t.Helper()

	hub := &Hub{
		Log: slog.New(slog.DiscardHandler),
		Authorize: func(_ context.Context, token string) (string, error) {
			if token != "good-token" {
				return "", errors.New("no")
			}

			return "conn-1", nil
		},
	}

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	r.URL = srv.URL
	r.Kind = "test"
	r.Version = "v-test"
	r.Log = slog.New(slog.DiscardHandler)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go r.Run(ctx)

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, ok := hub.Connected("conn-1"); ok {
			return hub, srv
		}
	}

	t.Fatal("the relay never connected")

	return nil, nil
}

func TestRelay(t *testing.T) {
	// A preview's service, next to the relay.
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello from "+r.URL.Path)
	}))
	t.Cleanup(service.Close)

	allowed := strings.TrimPrefix(service.URL, "http://")

	// The compute's API: it must see the relay's credentials, never Stagd's.
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	})

	hub, _ := connect(t, &Relay{
		Token: "good-token",
		API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("Authorization", "relay's own")
			api.ServeHTTP(w, r)
		}),
		Allow: func(addr string) error {
			if addr != allowed {
				return errors.New("not that one")
			}

			return nil
		},
	})

	info, _ := hub.Connected("conn-1")
	if info.Kind != "test" || info.Version != "v-test" {
		t.Fatalf("info: %+v", info)
	}

	client := &http.Client{Transport: hub.Transport("conn-1"), Timeout: 5 * time.Second}

	// Several requests, reusing connections through the relay.
	for range 3 {
		res, err := client.Get(service.URL + "/page")
		if err != nil {
			t.Fatal(err)
		}

		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		if string(body) != "hello from /page" {
			t.Fatalf("through the relay: %q", body)
		}
	}

	// The relay refuses what it doesn't allow, and says why.
	_, err := hub.Dial(context.Background(), "conn-1", "10.0.0.1:443")
	if err == nil || !strings.Contains(err.Error(), "not that one") {
		t.Fatalf("a refused address: %v", err)
	}

	// The API, with the relay's credentials.
	apiClient := &http.Client{Transport: hub.APITransport("conn-1"), Timeout: 5 * time.Second}

	req, _ := http.NewRequest(http.MethodGet, "http://compute/version", nil)
	req.Header.Set("Authorization", "Bearer from-stagd")

	res, err := apiClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if string(body) != "GET /version relay's own" {
		t.Fatalf("api: %q", body)
	}

	// Another key has no relay.
	if _, err := hub.Dial(context.Background(), "conn-2", allowed); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("no relay: %v", err)
	}

	// Dropped, it reconnects by itself.
	hub.Drop("conn-1")

	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if res, err := client.Get(service.URL + "/again"); err == nil {
			res.Body.Close()

			break
		}

		if time.Now().After(deadline) {
			t.Fatal("never reconnected")
		}
	}
}

func TestRelayRefusedToken(t *testing.T) {
	hub := &Hub{
		Log:       slog.New(slog.DiscardHandler),
		Authorize: func(context.Context, string) (string, error) { return "", errors.New("no") },
	}

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	r := &Relay{URL: srv.URL, Token: "bad", Log: slog.New(slog.DiscardHandler), Allow: func(string) error { return nil }}

	if err := r.serve(context.Background()); !errors.Is(err, errRefused) {
		t.Fatalf("a refused token: %v", err)
	}
}

func TestAllowKube(t *testing.T) {
	for addr, ok := range map[string]bool{
		"web.stagd-0192d4e8-7c1a-7b3e-9f00-4a2b6c8d0e1f.svc:80": true,
		"api.stagd-abc.svc.cluster.local:8080":                  true,
		"web.stagd-system.svc:80":                               false,
		"kubernetes.default.svc:443":                            false,
		"10.0.0.1:80":                                           false,
		"web.stagd-abc.svc.evil.com:80":                         false,
		"web.stagd-abc.svc":                                     false,
	} {
		if err := AllowKube(addr); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}

}
