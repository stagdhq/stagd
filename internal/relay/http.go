package relay

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Preview traffic can also reach the relay over plain HTTP, from a
// Cloudflare Tunnel (cloudflared) running next to it: Stagd's edge sends
// each request through the tunnel, naming the service it's for, and the
// relay proxies it there. Stagd's own traffic (the compute's API) stays on
// the relay's connection to Stagd.
const (
	// HeaderTarget is the service a request is for, e.g. web.stagd-<id>.svc:3000.
	HeaderTarget = "X-Stagd-Target"

	// HeaderAuth is the relay token's SHA-256, hex: what Stagd keeps of the
	// token, so only Stagd's edge can send requests through.
	HeaderAuth = "X-Stagd-Relay-Auth"
)

// TokenHash is the relay token's SHA-256, hex, as Stagd keeps it.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

// HTTP serves preview requests that come through the tunnel: each must
// carry the token's hash and name a service the relay may reach (Allow);
// anything else gets a 404 or 403 and goes no further.
func (r *Relay) HTTP() http.Handler {
	want := []byte(TokenHash(r.Token))

	proxy := &httputil.ReverseProxy{
		// As Stagd's edge proxies previews: the service's address, where the request came from kept.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: pr.In.Header.Get(HeaderTarget)})

			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				if v := pr.In.Header.Values(h); len(v) > 0 {
					pr.Out.Header[h] = v
				}
			}

			pr.Out.Header.Del(HeaderTarget)
			pr.Out.Header.Del(HeaderAuth)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			r.Log.Warn("relay: tunnel request", "target", req.Header.Get(HeaderTarget), "err", err)
			http.Error(w, "the preview's service didn't answer", http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if subtle.ConstantTimeCompare([]byte(req.Header.Get(HeaderAuth)), want) != 1 {
			http.NotFound(w, req)

			return
		}

		target := req.Header.Get(HeaderTarget)

		if r.Allow == nil || target == "" {
			http.NotFound(w, req)

			return
		}

		if err := r.Allow(target); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)

			return
		}

		proxy.ServeHTTP(w, req)
	})
}
