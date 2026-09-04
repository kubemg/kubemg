# Backend internals

Go, Gin, GORM, PostgreSQL 16, module `github.com/kubemg/kubemg/backend`. One
binary: it serves the API, the tunnel, the proxy and — in a production build —
the console itself out of `pkg/webui`.

## The store

`pkg/db` holds the models and the query layer. Six of them carry the access
model: `User`, `Cluster`, `UserClusterAccess`, `Group`, `UserGroup`,
`GroupClusterAccess`.

Three rules there are easy to break by accident:

- **The join tables pin their own `TableName()`.** GORM would otherwise
  pluralise `user_cluster_access`, `user_groups` and `group_cluster_access` into
  names the schema does not have.
- **`User.Role` is derived from `User.SystemRole`.** Always set SystemRole and
  let normalisation fill in Role. Writing Role directly produces an account
  whose JWT and whose stored privilege disagree.
- **`AccessForUser` returns *effective* access**, direct grants merged with
  everything inherited from the caller's groups, the more permissive grant
  winning. Nothing should re-derive that merge itself.

`backend/migrations/` is reference DDL that nothing executes. AutoMigrate
applies the schema at boot and the Go models win any disagreement.

## Configuration and settings

`pkg/config` reads the environment at boot. But an environment variable is a
**default**, not the value: most of them are overridable at runtime from the
settings surface, and code must read through the settings accessor rather than
the config struct it was booted with. Reading `s.publicURL` directly is the bug;
`s.settings(ctx)` is the fix.

The [Environment reference](../install/environment.md) and
[Runtime settings](../reference/settings.md) in the user guide are the
authoritative lists of both halves.

## Routes and authorisation

The surface is documented in full in the [REST API reference](api.md). What
matters when adding to it:

- Every list handler goes through the shared paged-response helper rather than
  writing JSON itself. Reads page because the agent caps a response frame, and
  an **empty page with a continue token is not the end of a list**. A compacted
  continue token comes back as `410 Gone`, which means truncation rather than
  failure.
- A caller can never delete, disable, or change the system role of **their own**
  account, and only a super admin manages another super admin.
- Read surfaces that are narrowed rather than refused — the audit trail, the
  kubeconfig register, terminal sessions — narrow a non-admin to their own rows
  silently, and a query parameter **cannot widen** that. Somebody else's row
  answers **404, not 403**: a 403 confirms the row exists.
- Routes with a literal segment that could be mistaken for an id are registered
  **before** the parameterised route. `POST /kubeconfigs/revoke-all` is
  registered before `/:id` so "revoke-all" is never read as an id.
- CORS must keep `Authorization` in the allowed headers, `PUT` and `PATCH` in
  the allowed methods, and `Cache-Control` in the allowed headers — the last one
  because it is how the console bypasses the read cache.

## Reading cluster state

`pkg/api/resources.go` and its neighbours answer every resource read through
`bastion.Proxy.Call`, which means the same impersonation, the same namespace
scope and the same audit record as a `kubectl` call. There is no second path
into a cluster, and adding one is the thing to avoid above all else.

Two rules there produce real bugs when missed:

- **All-namespaces and a single namespace are two different reads.** An
  unscoped caller gets the cluster-wide path. A namespace-scoped grant reads
  each granted namespace individually and merges. Answering all-namespaces for a
  scoped grant with a cluster-wide path leaks.
- **ConfigMaps and Secrets return keys only.** No value ever enters a response,
  and the describe walker reaches `spec` and `status` but never a Secret's
  `data`.

Counts are read at `limit=1` and taken from the API server's own
`remainingItemCount`, so their cost is flat in cluster size, and they are read
lazily rather than on a tick.

## Writes

Workload actions — scale, restart, suspend — are **read-modify-write, not a
patch**: the object is read, changed, and sent back with its `resourceVersion`,
so a concurrent change becomes a 409 rather than a silent overwrite. A request
for a state the object already holds is answered rather than written.

Deletes carry `propagationPolicy=Background` and the response says "marked for
deletion", not "deleted". There is deliberately **no bulk route**: a selection of
eight objects is eight sequential calls, each with its own audit record.

## The observability query path

`pkg/observability/query.go` is the one read where authorisation cannot be
delegated to the cluster, because the datasource has no idea who the caller is.

- **The browser never sends a query.** It sends a chart name, a window and a
  scope; the server writes the PromQL, LogsQL or LogQL.
- Names are validated against an anchored RFC 1123 pattern rather than escaped.
- A cluster-wide chart is refused to a namespace-scoped caller.
- The window is capped at 30 days.

## Audit

Every proxied call is recorded, refusals included, and a streaming call is
recorded twice — once when it opens and once when it closes. Persistence is
asynchronous and **drops on a full queue rather than blocking the proxy**.

Selective audit (`pkg/auditpolicy`) publishes an immutable snapshot the gateway
reads without a lock. Three things no selection suppresses: a refusal or error,
any streaming call, and kubemg's own replay and recording routes. An empty
selection means "every verb", not "none".

Forwarding (`pkg/auditforward`) is a fourth auditor that pushes the **complete**
trail as RFC 5424 syslog. It is deliberately not an alarm channel: the alarm
dispatcher deduplicates, cools off and drops, all of which lose records.
Selective audit is deliberately not applied to it, for the same reason it is not
applied to the structured log.

## Caching

`pkg/cache` is a short-TTL read cache, five seconds by default, keyed on a hash
of cluster, user, role, path and sorted query. Three properties keep it honest:
an entry is **never served to another identity**, only a `200` is stored, and any
non-GET invalidates the whole cluster scope. A negative TTL turns caching off
entirely.

## Leases

Several background jobs must run on exactly one replica: the alarm watcher, the
Helm index sync, the browser shell reaper. Each takes a conditional-upsert lease
in the database rather than a Postgres advisory lock, so the holder survives a
connection blip. **A store error while taking the lease means do not run**, not
run anyway.

The same "fail closed on nothing" rule governs every published snapshot in the
backend: a failed refresh keeps the previous value. It never empties and never
inverts.
