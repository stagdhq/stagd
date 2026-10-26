# Security

Please report security issues privately, by email to **security@stagd.dev**, not in a public issue or pull request. Include what you found, how to reproduce it, and what it affects (the CLI, `stagd relay`, the Kubernetes manifests, or the API contract). We'll reply within a few working days and keep you posted until it's fixed.

`stagd relay` runs inside customers' clusters and on their Docker hosts, so issues that let it reach more than its own `stagd-*` namespaces or preview containers, or let anyone but Stagd use its connection, matter most to us.

Only the latest release is supported.
