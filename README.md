# stagd

[Stagd](https://stagd.dev) gives every pull request a live preview of your app: built, started with its database and seed data, and on a URL your team and reviewers can open. Previews run on your own compute, sleep when nobody uses them, and go when the pull request does.

This repository is Stagd's open part:

- **`stagd`**, the command line: everything the dashboard does, for your terminal and scripts.
- **`stagd relay`**, which connects compute Stagd can't reach (a private Kubernetes cluster, a Docker host behind NAT) by dialling out to Stagd.
- **The Kubernetes manifests** a cluster gets, so you can read exactly what runs in it.
- **The API contract**, `stagd.api.v1`, which the CLI and the dashboard use, published at [buf.build/stagdhq/stagd](https://buf.build/stagdhq/stagd).

Stagd itself is hosted at [app.stagd.dev](https://app.stagd.dev).

## Install

On macOS and Linux:

```bash
curl -fsSL https://stagd.dev/install.sh | bash
```

On Windows, in PowerShell:

```powershell
irm https://stagd.dev/install.ps1 | iex
```

Either downloads the latest release for your machine (amd64 or arm64), checks it against the release's checksums, and installs `stagd`: in `~/.local/bin`, or on Windows in `%LOCALAPPDATA%\Programs\stagd`, which it adds to your PATH. `STAGD_INSTALL_DIR` puts it elsewhere, `STAGD_VERSION` installs another release. Or with Homebrew, or Go:

```bash
brew install stagdhq/tap/stagd
go install github.com/stagdhq/stagd/cmd/stagd@latest
```

`stagd update` updates it in place, the same way (Homebrew: `brew upgrade stagd`); `stagd update --check` only says whether there's a newer release. The relay runs from the image `ghcr.io/stagdhq/stagd`.

## Get started

```bash
stagd login                          # sign in through your browser; the session goes to your OS keychain
stagd orgs create acme --name "Acme" # or: stagd orgs list, stagd orgs switch <slug>
stagd github connect                 # install the Stagd GitHub App and link it to the org
stagd compute connect namespace      # where previews run: namespace, kubernetes or docker
stagd secrets set --repo acme/web SESSION_SECRET
stagd validate                       # check stagd.yaml and the compose file in a checkout
stagd preview create --repo acme/web --pr 42
stagd preview list
```

`stagd <command> --help` explains each one. `--json` prints JSON for scripts, and `--api-url` (`STAGD_API_URL`) points the CLI at another Stagd server.

## The relay

Some compute can't be reached from the internet, and shouldn't be. `stagd relay` runs next to it and opens one outbound connection to Stagd (a WebSocket over HTTPS); Stagd manages the compute and reaches previews through it. Nothing next to the relay needs to accept connections, and no cluster credentials leave it.

### Kubernetes

```bash
stagd compute connect kubernetes --name my-cluster
stagd compute install my-cluster | kubectl apply -f -
```

`stagd compute install` asks Stagd for the cluster's install and prints it for kubectl, with a new relay token: there's no link, and the token is only ever in the pipe. Running it again gives a new token and cuts off a relay still using the old one. `--wait` waits until the relay connects.

What it installs, from [`deploy/kubernetes`](deploy/kubernetes):

| Manifest | What it is |
|---|---|
| `stagd-access.yaml` | The `stagd-system` namespace and Stagd's service account. Cluster-wide it may only create namespaces and bind its own role in them; an admission policy keeps every change it makes to `stagd-*` namespaces, other than `stagd-system`. It can't read anything elsewhere. |
| `stagd-relay.yaml` | The relay, running as that account. It only connects Stagd to services in `stagd-*` namespaces. |
| `stagd-build.yaml` | Optional: a BuildKit daemon whose cache every preview shares, and a registry for what it builds, so building needs nothing set up. The daemon runs privileged; clusters that forbid that build elsewhere. |
| `stagd-tunnel.yaml` | Optional: `cloudflared`, for preview traffic through a Cloudflare Tunnel Stagd makes for the cluster, which is faster than through the relay's connection. It also only dials out; the relay checks every request it passes on. |

To remove Stagd from a cluster, delete the connection in Stagd (it refuses while previews still run on it), then, as a cluster admin:

```bash
stagd compute uninstall kubernetes | kubectl delete --ignore-not-found -f -
```

It prints everything an install can bring, without tokens. Stagd can't do this itself: it has no rights outside its own namespaces, by design.

### Docker

```bash
stagd compute connect docker --name build-box
```

prints the `docker run` command for the host, with the relay's token. Whoever reaches the Docker API controls the host, so use a host meant for previews. To remove it: `docker rm -f stagd-relay`.

## Develop

Everything runs with Go alone: `go tool task <name>` (Task is pinned in `go.mod`).

```bash
go tool task build      # bin/stagd
go tool task check      # what CI runs: protos, formatting, vet, tests
go tool task generate   # internal/gen from proto/ (needs buf)
go tool task image      # the image, as stagd:local
go tool task dist       # release files into dist/, as a v* tag publishes them
```

```
cmd/stagd/          the stagd binary
internal/
  cli/              the commands
  client/           the API client, device login and token storage
  output/           tables and JSON
  relay/            stagd relay, and its protocol
  gen/              generated from proto/, never edited by hand
proto/              the stagd.api.v1 contract (Buf)
deploy/kubernetes/  what a cluster gets
```

The image is the binary on distroless, as an unprivileged user (65532). CI publishes `main` as `:main` and `:sha-<commit>`, and `v*` tags as their version and `:latest`. A `v*` tag also makes the GitHub release, with the binaries, `install.sh`, `install.ps1` and `checksums.txt`, and updates the Homebrew formula in [stagdhq/homebrew-tap](https://github.com/stagdhq/homebrew-tap).

To release, tag with a one-line summary, written as it should read at the top of the notes: `git tag -a v0.5.0 -m "v0.5.0: What's in it"`, and push the tag. The release's notes, which are the changelog, are that summary and the commits since the last tag (`scripts/release-notes.sh`): `feat:` under New, `fix:` under Fixes, `deploy:`, `docs:`, `perf:` and `refactor:` under Also, and `chore:` left out. So write commit subjects for the notes, and use `feat:` or `fix:` for what users notice; `feat!:` marks a breaking change.

## Changes

See [the releases](https://github.com/stagdhq/stagd/releases). Contract changes are checked for breaking changes against what's published.

## Security

Please report security issues privately to security@stagd.dev; see [SECURITY.md](SECURITY.md).

## License

[Apache License 2.0](LICENSE).
