package relay

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Through the tunnel: with the token's hash and a service the relay may
// reach, the request goes there as Stagd's edge sends it; otherwise nowhere.
func TestRelayHTTP(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s host=%s fwd=%s target=%q auth=%q", r.Method, r.URL.RequestURI(), r.Host, r.Header.Get("X-Forwarded-Host"), r.Header.Get(HeaderTarget), r.Header.Get(HeaderAuth))
	}))
	t.Cleanup(service.Close)

	allowed, _ := url.Parse(service.URL)

	r := &Relay{
		Token: "stagd_relay_secret",
		Log:   slog.New(slog.DiscardHandler),
		Allow: func(addr string) error {
			if addr != allowed.Host {
				return fmt.Errorf("not %s", addr)
			}

			return nil
		},
	}

	front := httptest.NewServer(r.HTTP())
	t.Cleanup(front.Close)

	send := func(auth, target string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/a?b=1", nil)
		req.Header.Set(HeaderAuth, auth)
		req.Header.Set(HeaderTarget, target)
		req.Header.Set("X-Forwarded-Host", "pr-1.stagd.run")

		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()

		body, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(body)
	}

	good := TokenHash("stagd_relay_secret")

	if status, body := send(good, allowed.Host); status != http.StatusOK || body != fmt.Sprintf(`GET /a?b=1 host=%s fwd=pr-1.stagd.run target="" auth=""`, allowed.Host) {
		t.Fatalf("through: %d %s", status, body)
	}

	if status, _ := send("wrong", allowed.Host); status != http.StatusNotFound {
		t.Errorf("a wrong hash: %d", status)
	}

	if status, _ := send("", allowed.Host); status != http.StatusNotFound {
		t.Errorf("no hash: %d", status)
	}

	if status, _ := send(good, "kubernetes.default.svc:443"); status != http.StatusForbidden {
		t.Errorf("a service it may not reach: %d", status)
	}

	if status, _ := send(good, ""); status != http.StatusNotFound {
		t.Errorf("no target: %d", status)
	}
}
