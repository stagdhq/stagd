# The relay

Some compute can't be reached from the internet, and shouldn't be. `stagd relay` runs next to it and opens one outbound connection to Stagd (a WebSocket over HTTPS); Stagd manages the compute and reaches previews through it. Nothing next to the relay needs to accept connections, and no cluster credentials leave it.

## Kubernetes

```bash
stagd compute connect kubernetes --name my-cluster
stagd compute install my-cluster | kubectl apply -f -
```

`stagd compute install` asks Stagd for the cluster's install and prints it for kubectl, with a new relay token: there's no link, and the token is only ever in the pipe. Run it again to update the cluster (a newer relay, or after changing the connection's settings): while the relay is connected, it keeps its token, and the manifest leaves the cluster's `stagd-relay` secret as it is. `--new-token` gives the relay a new one and cuts off the old. `--wait` waits until the relay connects.

What it installs, from [`deploy/kubernetes`](../deploy/kubernetes):

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

## Docker

On the Docker host, signed in:

```bash
stagd compute connect docker --name build-box --run
```

makes the connection, starts its relay as the container `stagd-relay` on a network of its own, and waits until it connects. For a connection made elsewhere (in the dashboard, say), `stagd compute install build-box --run` gives the relay a new token and starts it the same way. With `--print` instead, both print docker's commands for the relay, with its token, to run yourself.

Whoever reaches the Docker API controls the host, so use a host meant for previews. To remove the relay, on the host: `stagd compute uninstall docker --run`.
