# Development

Everything runs with Go alone: `go tool task <name>` (Task is pinned in `go.mod`).

```bash
go tool task build      # bin/stagd
go tool task check      # what CI runs: protos, formatting, vet, tests
go tool task generate   # internal/gen from proto/ (needs buf)
go tool task image      # the image, as stagd:local
go tool task dist       # release files into dist/, as a v* tag publishes them
```

## Layout

```
cmd/stagd/          the stagd binary
internal/
  cli/              the commands
  client/           the API client, device login and token storage
  output/           tables and JSON
  relay/            stagd relay, and its protocol
  dockerup/         stagd docker up: a plan brought up on Docker
  gen/              generated from proto/, never edited by hand
proto/              the stagd.api.v1 contract and stagd.plan.v1 (Buf)
deploy/kubernetes/  what a cluster gets
docs/               these pages
```

## The contract

`proto/` holds `stagd.api.v1`, the API the CLI and the dashboard use, and `stagd.plan.v1`, a preview's plan. Both are published at [buf.build/stagdhq/stagd](https://buf.build/stagdhq/stagd), and changes are checked for breaking changes against what's published.

Releasing is in [releasing.md](releasing.md).
