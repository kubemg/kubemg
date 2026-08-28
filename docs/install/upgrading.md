# Upgrading

## Version pinning

Pin an explicit tag rather than tracking `latest`, in both places a version
appears:

- The management plane image, `KUBEMG_IMAGE`/`KUBEMG_VERSION`
  (`ghcr.io/kubemg/kubemg:0.8.3`) in Compose, or the `image:` field of the
  Deployment in Kubernetes.
- The agent image, `KUBEMG_AGENT_IMAGE` (`ghcr.io/kubemg/kubemg-agent:0.8.3`),
  written into every rendered agent install manifest by the management
  plane, so bumping it here is what changes what a *future* `kubectl apply -k
  …` installs — it does not touch agents already running.

- The browser shell image, `KUBEMG_SHELL_IMAGE`
  (`ghcr.io/kubemg/kubemg-shell:0.8.3`), which a shell pod runs on a target
  cluster. Like the agent image it is read when a shell is *started*, so
  bumping it changes the next shell rather than one already open.

All three images are published as multi-arch (amd64+arm64) manifest indexes by
`.github/workflows/release.yml` on a `v*` tag, after a Trivy vulnerability
gate, to `ghcr.io/kubemg/kubemg`, `ghcr.io/kubemg/kubemg-agent` and
`ghcr.io/kubemg/kubemg-shell`.

## Upgrading the management plane

```bash
# Docker Compose
docker compose pull
docker compose up -d

# Kubernetes
kubectl set image deployment/kubemg kubemg=ghcr.io/kubemg/kubemg:<new-version> -n kubemg
```

Schema migrations run automatically at boot via `db.Migrate` (AutoMigrate) —
there is no separate migration step to run before or after the image swap.
See [Database](database.md) for what that does and how the reference DDL in
`backend/migrations/` fits in if a DBA wants to review or pre-apply a change
under change control first.

**Keep the TLS certificate volume across the upgrade.** Whether it's the
self-signed pair kubemg minted or one you supplied, it must survive the
upgrade unchanged — every already-installed agent has that specific
certificate pinned into its trust bundle, and a fresh certificate (even a
correctly-configured one) is a *different* certificate that every existing
agent will refuse.

## Agent and server version compatibility

The tunnel handshake carries a `ProtocolVersion` that both sides must agree
on exactly — `backend/pkg/bastion/protocol.go` defines it on the server side
and `agent/internal/protocol` mirrors it (the agent is a separate
Apache-2.0 Go module and does not import the AGPL server, so the two copies are
kept in sync by hand and only need to agree on JSON field names and this
constant):

```go
// ProtocolVersion is bumped when a frame's meaning changes. The server refuses
// a handshake it does not recognise rather than guessing...
const ProtocolVersion = 2
```

The bastion **refuses a handshake at any other version** rather than
attempting to guess compatibility — an agent whose protocol version doesn't
match the server's will fail to connect entirely, not "half work." In
practice this means:

- Bumping `ProtocolVersion` is a **breaking change** to the wire format
  between kubemg and its agents. It only happens across the kind of release
  that would be flagged prominently (it added the v2 stream frames that carry
  `watch`, `logs -f`, `exec`, and `port-forward`).
- An agent significantly older than the management plane it's dialing may
  need to be upgraded before it can reconnect. Watch the release notes for a
  protocol bump when planning an upgrade across more than a couple of minor
  versions.
- There is no server-side compatibility shim across a protocol bump — the
  fix is upgrading the agent, which is a `kubectl apply -k …` of the
  install package rendered by the *upgraded* server (or updating
  `KUBEMG_AGENT_IMAGE` and re-applying), not a config change.

## When agents must re-apply their manifests

Separately from the wire protocol, the agent's Kubernetes manifests
(`ClusterRoleBindings`, and the `ClusterRole`s they bind to) can gain new
permissions between releases without any protocol change at all. When they
do, **existing agent installs must re-apply their manifests** to pick up the
new grants; until they do, the symptom is silent and specific rather than a
tunnel that visibly fails.

It has happened three times so far:

- **CRD discovery and custom-resource read/write RBAC.** Without it, CRD
  discovery answers `403` and the Explore sidebar simply shows no custom
  resources, with no error surfaced anywhere obvious.
- **0.8.1, the browser shell.** The `kubemg-shell-runner` Role and its
  binding to the `kubemg:shell-runner` user — what lets KubeMG create, seed,
  stamp and delete shell pods **in the agent namespace only** — arrived with
  that release. Without them nothing else changes: the tunnel stays up and
  every existing surface keeps working, but opening a shell fails on the
  cluster's own `403` at pod creation. Everything a KubeMG upgrade brings
  except the shell works on an install that re-applies nothing.
- **0.8.3, the shell's exec verb.** The same Role granted only `create` on
  `pods/exec`. An exec is opened over a WebSocket, which begins as a GET, and
  the API server authorizes that as `get` on the subresource — so on 0.8.1 and
  0.8.2 the shell pod starts and then fails with `403 Forbidden` while writing
  its kubeconfig. Re-applying the manifests adds the missing verb.

Re-applying is the same command as installing. The console renders it for a
cluster that already exists: open the cluster's dashboard and choose **Agent
install** (admin-only, agent-mode clusters), which re-renders the package
from the cluster's stored registration token against the current settings —
so it carries the new agent image as well as the new RBAC. The Kustomize form
fetches and extracts the package first, because Kustomize accepts only local
paths and Git specs as remote targets:

```bash
curl -sfL https://your-kubemg/install/<token>/kustomize.tar.gz | tar -xz
kubectl apply -k kubemg-agent
```

or apply the flat manifest, which is the one-liner the console shows first:

```bash
kubectl apply -f https://your-kubemg/install/<token>/agent.yaml
```

If you manage the manifests yourself rather than through the rendered
package, diff `deploy/kustomize/base/rbac.yaml` at the new version against
what's applied and reconcile. Both the cluster detail page and the wizard's
last step in the console call out whether an attached cluster's RBAC is
current.

## Rollback

There is no destructive migration to roll back — `AutoMigrate` only adds
tables and columns, it never drops or renames them, so a database migrated
forward by a newer version is still a valid schema for an older one to run
against (it will simply not use the columns/tables it doesn't know about).
Rolling back the management plane image is therefore safe from a schema
perspective:

```bash
docker compose pull ... # (pin KUBEMG_IMAGE back to the previous tag)
docker compose up -d
```

Two things a rollback does **not** undo:

- **A protocol bump.** If the version you're rolling back from introduced a
  new `ProtocolVersion`, agents already re-applied against it will fail to
  handshake against the older server until they are rolled back too (or, more
  practically, re-applied against the older server's rendered manifest,
  which pins the compatible agent image again).
- **Data written under a newer schema's meaning** — this is a general
  database caveat, not a kubemg-specific one, and is why testing an upgrade
  against a restored copy of production before doing it for real is worth
  the time it takes.

## Documentation versioning

This manual is versioned against release tags on Read the Docs: an install
running `0.8.3` corresponds to the `0.8.3` version of these docs, not
whatever `master` says today. If you're following a procedure here, check
the version selector matches the version you're actually running.

## Next

- [Database](database.md)
- [Production checklist](production-checklist.md)
- [The agent](../clusters/agent.md)
