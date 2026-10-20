package relay

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// Where a pod finds its service account.
const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InCluster reports whether the relay runs in a Kubernetes pod.
func InCluster() bool {
	_, err := os.Stat(serviceAccountDir + "/token")

	return err == nil && os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// KubeAPI forwards Stagd's calls to the cluster's API server, as the pod's
// service account: whatever Stagd sends for credentials is dropped.
func KubeAPI() (http.Handler, error) {
	ca, err := os.ReadFile(serviceAccountDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("relay: the service account's CA: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("relay: the service account's CA isn't PEM")
	}

	target := &url.URL{Scheme: "https", Host: net.JoinHostPort(os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"))}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host

			// Projected tokens are rotated: read it fresh each time.
			token, _ := os.ReadFile(serviceAccountDir + "/token")
			pr.Out.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		},
	}, nil
}

// previewService is a service in a preview's namespace: <service>.stagd-<id>.svc[.cluster.local]:<port>.
var previewService = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?\.(stagd-[a-z0-9]([-a-z0-9]*[a-z0-9])?)\.svc(\.cluster\.local)?:[0-9]{1,5}$`)

// AllowKube lets Stagd dial services in preview namespaces, and nothing else in the cluster.
func AllowKube(addr string) error {
	m := previewService.FindStringSubmatch(addr)
	if m == nil || m[2] == "stagd-system" {
		return fmt.Errorf("the relay only connects to services in stagd-* namespaces, not %s", addr)
	}

	return nil
}
