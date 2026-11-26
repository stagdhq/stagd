<div align="center">
  <a href="https://stagd.dev">
    <img src=".github/assets/stagd.svg" alt="Stagd" width="72" height="72">
  </a>
  <h1>Stagd</h1>
  <p><strong>A live, seeded preview of every pull request, on your own compute.</strong></p>

  <p>
    <a href="https://github.com/stagdhq/stagd/releases/latest"><img src="https://img.shields.io/github/v/release/stagdhq/stagd?label=release&color=166138" alt="Latest release"></a>
    <a href="https://github.com/stagdhq/stagd/actions/workflows/image.yaml"><img src="https://img.shields.io/github/actions/workflow/status/stagdhq/stagd/image.yaml?branch=main&label=build" alt="Build"></a>
    <a href="https://buf.build/stagdhq/stagd"><img src="https://img.shields.io/badge/buf-stagdhq%2Fstagd-166138" alt="Buf"></a>
    <a href="LICENSE"><img src="https://img.shields.io/github/license/stagdhq/stagd?color=166138" alt="License"></a>
  </p>

  <p>
    <a href="https://stagd.dev"><strong>Website</strong></a> ·
    <a href="https://stagd.dev/docs/"><strong>Docs</strong></a> ·
    <a href="https://stagd.dev/docs/quickstart/"><strong>Quickstart</strong></a> ·
    <a href="https://stagd.dev/docs/reference/cli/"><strong>CLI reference</strong></a> ·
    <a href="https://app.stagd.dev"><strong>Dashboard</strong></a>
  </p>
</div>

<br>

[Stagd](https://stagd.dev) gives every pull request a live preview of your app: built, started with its database and seed data, and on a URL your team and reviewers can open. Previews run on your own compute, sleep when nobody uses them, and go when the pull request does.

This repository is Stagd's open part: the `stagd` command line, the relay that connects private clusters and Docker hosts, the Kubernetes manifests a cluster gets, and the API contract.

## Why Stagd?

- **[Your compute](https://stagd.dev/docs/compute/overview/):** previews run on Namespace, your Kubernetes cluster or a Docker host, not ours. Private ones connect through `stagd relay`, which only dials out.
- **[Real data](https://stagd.dev/docs/guides/seed-data/):** each preview starts with its database and seed data, so reviewers see the app as it really works.
- **[From your compose file](https://stagd.dev/docs/guides/describe-your-app/):** `stagd init` writes a `stagd.yaml` from the compose file you have. Stagd is a GitHub App, so there's no workflow to add.
- **[Built for coding agents](https://stagd.dev/docs/guides/coding-agents/):** `stagd mcp` lets Claude Code, Cursor and other agents make a preview, wait for it, and read its screenshot, logs and mail.

## Quickstart

```bash
curl -fsSL https://stagd.dev/install.sh | bash
stagd login
stagd init
```

`stagd login` signs you in through your browser, and `stagd init` writes a `stagd.yaml` in your checkout. With GitHub and your compute connected ([the quickstart](https://stagd.dev/docs/quickstart/) walks through both), push it, and the next pull request gets a preview.

## Install

<details>
<summary>Windows, Homebrew and Go</summary>

On Windows, in PowerShell:

```powershell
irm https://stagd.dev/install.ps1 | iex
```

With Homebrew, or Go:

```bash
brew install stagdhq/tap/stagd
go install github.com/stagdhq/stagd/cmd/stagd@latest
```

</details>

`stagd update` keeps it up to date. Every way to install and update is in [docs/install.md](docs/install.md).

## For coding agents

```bash
claude mcp add stagd -- stagd mcp
```

Or in an `mcp.json`: `{ "mcpServers": { "stagd": { "command": "stagd", "args": ["mcp"] } } }`. The tools run on Stagd's server, signed in as you; `stagd mcp` carries the client's calls there. See [the MCP reference](https://stagd.dev/docs/reference/mcp/).

## What's in this repository

| | |
|---|---|
| **`stagd`** | The command line: everything the dashboard does, for your terminal and scripts. [Reference](https://stagd.dev/docs/reference/cli/) |
| **`stagd relay`** | Connects compute Stagd can't reach, a private cluster or a Docker host behind NAT, by dialling out. [docs/relay.md](docs/relay.md) |
| **`stagd docker up`** | Brings a preview up from its plan, on Namespace and Docker hosts and on your own Docker. [docs/plan.md](docs/plan.md) |
| **[`deploy/kubernetes`](deploy/kubernetes)** | The manifests a cluster gets, so you can read exactly what runs in it. |
| **[`proto/`](proto)** | The API contract, `stagd.api.v1`, and a preview's plan, `stagd.plan.v1`, published at [buf.build/stagdhq/stagd](https://buf.build/stagdhq/stagd). |

More in [docs/](docs).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), and [docs/development.md](docs/development.md) to build and test it. Changes are in [the releases](https://github.com/stagdhq/stagd/releases).

## Security

Please report security issues privately to security@stagd.dev; see [SECURITY.md](SECURITY.md).

## License

[Apache License 2.0](LICENSE).
