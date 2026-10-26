// Package kubernetes holds Stagd's Kubernetes manifests, for the CLI to
// print: `stagd compute uninstall kubernetes | kubectl delete --ignore-not-found -f -`
// removes all of them. Installs come from Stagd (CreateInstallManifest),
// with the connection's tokens.
package kubernetes

import "embed"

//go:embed *.yaml
var Files embed.FS

// Uninstall is every manifest an install can bring, for kubectl delete:
// what runs in stagd-system first, Stagd's access and rights last.
var Uninstall = []string{"stagd-tunnel.yaml", "stagd-build.yaml", "stagd-relay.yaml", "stagd-access.yaml"}
