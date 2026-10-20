package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// DockerSocket is where the relay finds the Docker engine.
const DockerSocket = "/var/run/docker.sock"

// HasDocker reports whether the relay can reach a Docker engine.
func HasDocker() bool {
	_, err := os.Stat(DockerSocket)

	return err == nil
}

func dockerTransport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", DockerSocket)
		},
	}
}

// DockerAPI forwards Stagd's calls to the Docker engine. Whoever can call it
// controls the host: run the relay on a Docker host meant for previews.
func DockerAPI() http.Handler {
	target := &url.URL{Scheme: "http", Host: "docker"}

	return &httputil.ReverseProxy{
		Transport: dockerTransport(),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Del("Authorization")
		},
	}
}

// previewAlias is a service on a preview's network: <service>.stagd-<id>:<port> (see compute.DockerAlias).
var previewAlias = regexp.MustCompile(`^[a-z0-9][-a-z0-9_]*\.(stagd-[a-z0-9]+):[0-9]{1,5}$`)

// DockerAllow lets Stagd dial services on preview networks, and nothing
// else. The relay runs in a container; it joins the preview's network
// first, so the alias resolves. Container is the relay's own container.
func DockerAllow(container string) func(addr string) error {
	client := &http.Client{Transport: dockerTransport(), Timeout: 15 * time.Second}

	return func(addr string) error {
		m := previewAlias.FindStringSubmatch(addr)
		if m == nil {
			return fmt.Errorf("the relay only connects to services on preview networks, not %s", addr)
		}

		network := m[1] + "_default"
		body := fmt.Sprintf(`{"Container":%q}`, container)

		res, err := client.Post("http://docker/networks/"+network+"/connect", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			return fmt.Errorf("joining %s: %w", network, err)
		}
		defer res.Body.Close()

		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))

		// Joined already is fine.
		if res.StatusCode >= 300 && !strings.Contains(string(raw), "already exists") {
			return fmt.Errorf("joining %s: %s", network, strings.TrimSpace(string(raw)))
		}

		return nil
	}
}

// OwnContainer is the container the relay runs in: STAGD_RELAY_CONTAINER, or
// the host name, which Docker sets to the container's id.
func OwnContainer() string {
	if c := os.Getenv("STAGD_RELAY_CONTAINER"); c != "" {
		return c
	}

	host, _ := os.Hostname()

	return host
}
