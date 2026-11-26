# A preview's plan

A plan (`stagd.plan.v1.Plan`, as JSON) is what a preview runs, whatever compute runs it: its services, builds, volumes and ingresses, where its source is, and the names of the environment variables holding its secrets, never their values. Its contract is in [`proto/stagd/plan/v1`](../proto/stagd/plan/v1), published at [buf.build/stagdhq/stagd](https://buf.build/stagdhq/stagd).

On Namespace and Docker hosts, Stagd puts the deploy's plan on the instance and runs `stagd docker up` there. Run it yourself to bring the same preview up on your own Docker, your checkout as the source:

```bash
stagd docker up plan.json --source .
```

It builds side by side, as many as there are CPUs, while images are pulled, then starts it with compose, publishing ingresses from port 18000 in the plan's order. Where it stands goes to `.stagd/state`, the logs beside it: `build-<service>.log`, `pull-<service>.log`, `up.log`.
