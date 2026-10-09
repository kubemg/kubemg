# Helm releases

Explore's **Helm** section lists, installs, upgrades, rolls back and uninstalls Helm releases on a cluster, and the same page covers the chart repositories installs come from. Use it to manage a release without a `helm` binary.

A Helm 3 release is a `Secret` labelled `owner=helm`. kubemg reads those Secrets and writes rendered objects through the same impersonated, audited tunnel as every other read and write. Helm's chart engine is used as a library; Helm's own Kubernetes client is not.

## Reading releases

The list shows each release once, at its **highest revision** — the one describing what is installed.

Reading a release means reading a Secret, and the built-in `view` role excludes Secrets. A `view` grant is therefore refused here in the cluster's own words. That is the correct answer, not a gap. In agent mode you need `edit` or `cluster-admin` to see releases.

| Shown | Never shown |
|---|---|
| Name, namespace, chart name and version, app version, revision, status, updated time | The chart's **rendered manifest** (it often holds generated passwords) |
| The values the operator supplied (what `helm get values` prints, without chart defaults) | |

A release opens in the detail drawer on two panels: **values** and **history**.

## Installing a chart

Install from a [registered repository](#chart-repositories). The version is resolved against the stored catalogue, so an install cannot be steered at an arbitrary URL, and a published digest is checked against the download. If a release of that name exists the install is refused (`409`); upgrade it instead.

- Rendering is Helm's own: values merging, subcharts, `.Release`, `tpl` and sprig. `.Capabilities` and `.APIVersions` come from the target cluster through the tunnel.
- Objects are written one at a time in Helm's order: `crds/`, pre-install hooks, the release, post-install hooks. Each object is its own audit record, so a forty-object chart is forty rows.
- CRDs from `crds/` are not recorded on the release, as with Helm.
- The manifest editor's deny list (RBAC kinds, Node) does **not** apply. Charts normally ship a ServiceAccount, ClusterRole and binding.
- A namespace-scoped grant is checked **before the first write**. A chart with a cluster-scoped object, or one targeting a namespace outside the grant, is refused with the object named and nothing is written.

!!! warning "A failed install is recorded and does not roll back"
    A failed write stops the run. The release is recorded as `failed`, and the response lists which objects were written and which were not. Objects that succeeded are not deleted, and there is no `--atomic`.

## Upgrading a chart

`POST .../resources/helm/releases/:name/upgrade` re-renders against a new version or values and applies the difference.

- Built-in kinds are merged so fields added by other controllers (an injected sidecar, an allocated `clusterIP`) survive. Custom resources get a JSON merge patch.
- A field the chart stopped rendering is removed. A field the chart never wrote is left alone.
- Writes carry the object's `resourceVersion`, so a concurrent change gives `409` instead of being overwritten.
- Objects the previous revision wrote and this one does not are deleted last. A failed delete there does not fail the upgrade.

??? info "Why it works this way"
    The merge is three-way: original is what the previous revision rendered, modified is this render, live is what the cluster holds. Writes are a full `PUT` because the tunnel carries one content type.

## Writing values only

`PUT .../resources/helm/releases/:name/values` works like `helm upgrade --reuse-values`: it reads the chart stored on the release, re-renders it with the new values and applies the result as an upgrade does. No repository needs to be reachable, because Helm stores the whole chart on the release.

!!! note "The one case that cannot be rendered"
    If the release's Secret has no stored chart, the write only appends a new revision (`sh.helm.release.v1.<name>.v<n+1>`) carrying the previous chart and manifest forward. The cluster keeps running what it ran before. The response's `helmValuesWarning` says so whenever it applies.

## History and rollback

`GET .../helm/releases/:name/history` returns every stored revision, newest first.

`POST .../helm/releases/:name/rollback` is `helm rollback`. It applies the target revision's **stored manifest** (it does not re-render) and records a new revision holding the target's chart, config and manifest.

| Request | Answer |
|---|---|
| A revision Helm has pruned | `404` ("has no revision `<n>` — Helm may have pruned it") |
| The current revision | `409` ("already the current one") |
| A revision with no stored manifest (old, or written by the values-only fallback) | `409` naming the reason |

## Uninstalling a release

`DELETE .../resources/helm/releases/:name` removes the release and what it installed. The release's recorded manifest is parsed server-side and its objects are deleted one at a time, each with its own audit record. The response is a per-object report.

- Objects are removed in **reverse install order**.
- The release's own Secrets go **last, and only if every object went**. If something cannot be removed, the release stays so you can retry. The response carries `removed: false` and names the first object left behind.
- A namespace-scoped grant is refused **before the first delete**, with the object named.
- A release whose manifest cannot be read is refused (`409`).

!!! warning "What an uninstall leaves behind"
    - **`pre-delete` and `post-delete` hooks are not run.** The release records its rendered manifest, not the templates. The response says so every time.
    - **Anything the chart did not render stays**: PVCs a StatefulSet expanded, objects a controller created, and CRDs from `crds/`. `helm uninstall` leaves these too.

    There is no `--keep-history` mode.

The uninstall sheet shows both limits before the button.

## Honest limits

!!! warning "What kubemg's Helm engine does not do"
    - **Hooks are applied in weight order but not waited on.** A chart that depends on a hook finishing may briefly look different from `helm install`. `hook-delete-policy` is ignored and `test` hooks never run. Responses for charts with hooks carry `hook_notice`.
    - **OCI registries are not read.** Repositories are `http(s)` only.

## Chart repositories

A chart repository tells kubemg where an `index.yaml` lives and how to reach it. Installs draw only from these.

### Server-wide, not per cluster

Repositories are registered once for the whole installation, not per cluster. Add an internal mirror once.

### Who may read, who may write

| Action | Who |
|---|---|
| Read the catalogue (`GET /api/v1/helm/repositories`, `.../:name/charts`) | Any signed-in user |
| Add, edit, delete, sync (`PUT`/`DELETE /api/v1/helm/repositories/:name`, `POST .../:name/sync`) | Admin only |

Adding a repository is an outbound-egress decision, like adding an [alarm channel](../audit/alarms.md).

### Credential handling

The API reports only `has_credential`. Saving with the credential field **omitted keeps** the stored one; an **empty string clears** it.

### What kind of repository

Only `http://` and `https://` are accepted.

- `oci://` is refused with its own message ("not a repository kind kubemg reads yet").
- `file://` is refused outright.

### Saving one

Saving fetches `index.yaml` immediately and reports the result. **The row is stored even if the fetch fails**, with `status: "error"` and the reason, so you can register a mirror before the network path is open. `status` is `pending`, `ok` or `error`. `POST .../:name/sync` re-runs the fetch on demand.

### The starter catalogue

A first boot seeds six repositories (ingress-nginx, jetstack, prometheus-community, grafana, bitnami, argo) as ordinary rows, editable and deletable, with `status: pending`. A deleted one does not return on restart. An air-gapped site deletes them all and adds its internal mirror.

### Fetching the index: scheduled, leased, bounded

Each repository is refreshed hourly, the first pass 2 minutes after boot. In a multi-replica install exactly one replica fetches, on a lease. If the lease cannot be read, that pass is skipped.

| What | Bound |
|---|---|
| Decompressed index size | 96 MB, refused past it |
| Versions kept per chart | newest 5 |
| Charts kept per repository | 5000 |

- Library charts and versions with no archive URL are dropped.
- "Newest" is by semver, not publication date. Installs offer the newest non-prerelease first; a chart with only prereleases falls back to the newest of those.
- **A failed sync never empties the catalogue.** A repository keeps its last good chart list and shows why the latest attempt failed.
