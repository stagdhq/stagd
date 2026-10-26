# stagd

The command line for [Stagd](https://stagd.dev): live, seeded previews of every pull request, on your own compute. It talks to a Stagd server over its API (`proto/`), and runs `stagd relay`, Stagd's way into compute it can't reach.

## Layout

```
cmd/stagd/          the stagd binary
internal/
  cli/              commands (urfave/cli, huh, lipgloss)
  client/api/       Connect client for stagd.api.v1
  client/auth/      device login, token storage (OS keychain)
  output/           table and JSON output
  relay/            stagd relay, and the relay protocol
  gen/              generated Go (protobuf + connect-go), never edited by hand
proto/              the stagd.api.v1 contract (Buf)
deploy/kubernetes/  Stagd's access to a cluster, and the relay in it
```

## The CLI

```bash
stagd login                          # device login in the browser; the token goes to the OS keychain
stagd orgs list | switch <slug>
stagd github connect                 # install the Stagd GitHub App, link it to the org
stagd repos list
stagd compute connect namespace      # or kubernetes, docker; see stagd compute providers
stagd integrations connect neon      # a database branch per preview
stagd secrets set --repo owner/name SESSION_SECRET
stagd preview create | list | watch | mail
stagd validate                       # check stagd.yaml and the compose file, as previews read them
stagd activity
```

`--api-url` (`STAGD_API_URL`) says which Stagd server; `--json` prints JSON.

## The relay

`stagd relay` runs next to compute Stagd can't reach, and dials out to it: Stagd manages the compute and sends preview traffic through that one connection, so nothing next to the relay needs to be reachable from outside.

- **Kubernetes:** `deploy/kubernetes/stagd-access.yaml` makes Stagd's service account, kept to `stagd-*` namespaces by an admission policy; `stagd-relay.yaml` runs the relay as it. The relay only connects Stagd to services in `stagd-*` namespaces. `stagd compute connect kubernetes` (or the dashboard) prints one `kubectl apply -f <link>` that installs both with the relay's token; `stagd compute install <name>` makes a new one, and `--wait` waits for the relay to connect. The link also brings, as the connection chooses: `stagd-build.yaml`, a shared BuildKit daemon and a registry for building in the cluster; and `stagd-tunnel.yaml`, a Cloudflare Tunnel Stagd makes for the cluster, so preview traffic goes from Stagd's edge to the relay on Cloudflare's network rather than through Stagd's server (the relay takes it on `--http`).
- **Docker:** run it on the host with the Docker socket. Whoever reaches the Docker API controls the host: use a host meant for previews.

```bash
docker run -d --name stagd-relay --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e STAGD_URL=https://stagd.example.com -e STAGD_RELAY_TOKEN=stagd_relay.… \
  ghcr.io/stagdhq/stagd:latest relay
```

## Removing Stagd from a cluster

Deleting a Kubernetes connection in Stagd removes what Stagd made outside the cluster (its Cloudflare Tunnel and DNS record), and Stagd refuses while previews still run on it. What the install command put in the cluster stays: Stagd's account may only manage `stagd-*` namespaces, so removing its own rights is the cluster's admins' job. `stagd compute delete` prints the same:

```sh
kubectl delete namespaces -l stagd.dev/managed=true   # previews left behind, if any
kubectl delete namespace stagd-system                 # the relay, cloudflared, the builder and registry, their volumes
kubectl delete clusterrolebinding stagd
kubectl delete clusterrole stagd stagd-preview
kubectl delete validatingadmissionpolicybinding stagd-namespaces
kubectl delete validatingadmissionpolicy stagd-namespaces
```

On a Docker host: `docker rm -f stagd-relay`.

## Develop

Go only: `go tool task <name>` (Task is pinned in go.mod). `buf` regenerates `internal/gen` from `proto/`.

```bash
go tool task build      # bin/stagd
go tool task check      # what CI runs
go tool task image      # the image, as stagd:local
```

The image, `ghcr.io/stagdhq/stagd`, is the binary on distroless as an unprivileged user (65532); CI publishes main as `:main` and `:sha-<commit>`, and `v*` tags as their version and `:latest`.
