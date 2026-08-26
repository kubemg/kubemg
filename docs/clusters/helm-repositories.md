# Chart repositories

Installing a chart needs somewhere to get it from. A **chart repository** is
where kubemg is told an `index.yaml` exists and how to reach it — the same
kind of fact a [datasource](../observability/datasources.md) is, except what
it describes is not a series backend but a source of executable templates.

## Server-wide, not per cluster

Repositories live in `helm_repositories`, one row per repository,
server-wide — **not per cluster**. What this installation is allowed to
reach out to over the network and download templates from is a fact about
the installation, not about any one cluster it manages: duplicating the
registration once per cluster would mean adding an internal mirror once per
cluster, for no reason a cluster boundary explains.

## Who may read, who may write

Reading the catalogue (`GET /api/v1/helm/repositories`, `GET
/api/v1/helm/repositories/:name/charts`) is open to **any signed-in user** —
an install form must not discover which repositories exist, or which charts
they hold, by being refused. Writing (`PUT`/`DELETE
/api/v1/helm/repositories/:name`, `POST
/api/v1/helm/repositories/:name/sync`) is **admin only**: adding a
repository is an outbound-egress decision, the same class of act as adding
an [alarm channel](../audit/alarms.md).

## Credential handling

A repository's credential is treated exactly like a cluster's service
account token or a datasource's: **stored, `json:"-"`, never serialised.**
The API reports `has_credential`, a boolean, and nothing else. Saving a
repository with the credential field omitted **keeps whatever is stored**;
sending it as an empty string **clears it**.

## What kind of repository

Only `http://` and `https://` are accepted.

- `oci://` is refused with its own message — *"not a repository kind kubemg
  reads yet"* — rather than a generic "unsupported scheme", because it names
  a real gap rather than implying every non-`http` address is a typo.
- `file://` is refused outright: accepting it would make the registration
  form a reader of the bastion process's own filesystem, which is not what
  anyone adding a repository is asking for.

## Saving one

`PUT /api/v1/helm/repositories/:name` fetches the repository's `index.yaml`
synchronously and reports what happened — but **the row is stored even when
the fetch fails**, with `status: "error"` and the reason. An operator adding
a repository before the network path to it is open — a private mirror not
yet reachable from this bastion, a firewall rule not yet applied — needs the
registration to succeed so the fetch can be retried later, not to be told
the whole operation failed because of something that isn't wrong with the
registration itself.

`status` is one of `pending` (nothing fetched yet), `ok`, or `error`.
`POST /api/v1/helm/repositories/:name/sync` re-runs the fetch on demand,
outside the schedule below.

## The starter catalogue

A first boot seeds six well-known repositories — ingress-nginx, jetstack,
prometheus-community, grafana, bitnami, argo — as **ordinary rows**: fully
editable and deletable, not pinned or specially protected. Seeding writes
them with `status: pending` and touches no network itself; the first
scheduled sync (or a manual one) is what actually fetches each index.

A `settings` marker (`helm_repositories_seeded`) makes this a **one-time**
act — a repository an operator deliberately deleted does not reappear on
the next restart. That marker is also the whole air-gapped story: a site
with no route to any of the six public repositories deletes all of them and
points the same feature at its internal mirror instead. Nothing about the
feature changes; only which rows are in the table does.

## Fetching the index: scheduled, leased, bounded

### One replica, on a lease

Exactly one replica of a multi-replica installation fetches any given
repository's index — `db.LeaseHelmIndex`, the same mechanism [the alarm
watcher](../audit/alarms.md) uses to poll exactly once. N replicas each
independently pulling a public repository's `index.yaml` — some of these run
past 60 MB — is a bandwidth bill and a rate limit nobody asked for by
running two replicas for availability, not a correctness problem that needs
solving twice.

A store error reading the lease means **do not poll this pass** — the same
rule the alarm watcher follows, and for the same reason: a replica that
cannot confirm it holds the lease must not guess that it does.

The schedule: every repository is refreshed on a 1-hour interval, the first
pass runs 2 minutes after boot (not immediately — a process still coming up
has other things to finish first), and the lease TTL is 3× the poll
interval.

### Bounds

| What | Bound |
|---|---|
| Decompressed index size | 96 MB — refused past it |
| Versions kept per chart | newest 5 |
| Charts kept per repository | 5000 |

Library charts and any version with no archive URL are dropped rather than
offered and then refused at install time — a chart that cannot be installed
should not be a chart that appears in the picker.

**Newest** is decided by semver, never by publication date: a version
2.0.0 published before a backported 1.2.4 must not be shadowed by the
backport just because it landed in the index later. The version an install
form offers first is the newest **non-prerelease** version; a chart that has
only ever published prereleases falls back to the newest of those.

### A failed sync does not empty the catalogue

The stored chart list for a repository is replaced **only by a fetch that
succeeded**. A repository that goes unreachable — a DNS blip, an expired
certificate, a network partition to an internal mirror — keeps every chart
it last successfully held, and the row records why the most recent attempt
failed. A chart list that empties the moment a repository can't be reached
reads as "this feature is broken"; a stale list with a visible error reads
as what actually happened.

## See also

[Helm releases](helm.md) for how a chart, once resolved from a repository
here, is installed, upgraded and rolled back.
