# Audit trail

Every call the bastion proxies to a cluster is recorded, whether the cluster
answered it or kubemg refused it before it ever left the building. The trail
is the single place that answers "who did what, to which cluster, and did it
work" — for a person looking, or for a SIEM tailing the process's own log.

## What is recorded

Two things write to the trail, through the same `Auditor` interface
(`pkg/bastion/audit.go`):

- Every call `bastion.Proxy.Call` makes on a user's behalf — list, get,
  watch, create, update, patch, delete, and the streaming verbs.
- kubemg's own sensitive reads of itself: watching or deleting a session
  recording, and every just-in-time access workflow event
  (`jit-request`/`jit-approve`/`jit-reject`/`jit-revoke`/`jit-expire`). See
  [Session recording](session-recording.md) and [JIT elevated access](../access/jit.md).

A refusal is recorded exactly like a success — a guardrail block, a
namespace-scope violation, a tunnel failure that never reached the API
server. There is no separate "denied" table; a denial is just a row whose
`status` is 4xx/5xx or whose `error` is non-empty.

## The record

`bastion.Event` (in-flight) is flattened into `db.AuditEvent` (the stored
row, `pkg/db/models.go`) by `toAuditRow`. The fields, deliberately kept flat
rather than nested — a trail is read by a SIEM far more often than by a
person, and nesting buys nothing:

| Field | Meaning |
|---|---|
| `at` | When the call happened (indexed, sorted newest first) |
| `user_id` / `username` | The kubemg account that made the call |
| `cluster_id` / `cluster` | Which cluster |
| `verb` / `method` | The Kubernetes verb (`list`, `get`, `create`, `exec`, …) and the raw HTTP method — see [Verb naming](#verb-naming) below |
| `path` | The full API path with its query string, so `kubectl get pods -w` reads differently from a plain list |
| `namespace` / `resource` | Parsed out of the path when it looks like a Kubernetes API URL |
| `impersonated_user` / `impersonated_groups` | The identity kubemg asserted to the cluster's API server — the field that ties a kubemg account to the Kubernetes subject that actually acted |
| `status` | The HTTP status the call ended with |
| `duration_ms` | How long the call took |
| `streaming` / `phase` | Whether this is a long-lived call, and whether this row is its open or its close — see below |
| `bytes_out` / `bytes_in` | Filled on the closing record of a stream: what came back from the cluster, and what the user sent into it |
| `session_id` | Correlates the two records of one interactive session, and is the join to that session's recording (see [Session recording](session-recording.md)) |
| `guardrail_policy` / `guardrail_action` | Which safety policy matched and what it did (`block` or `warn`) — see [Guardrails](../access/guardrails.md) |
| `error` | Set when the call never reached the API server (a refusal, a tunnel failure) |
| `diff` | The field-level diff of a manifest write, present only when manifest diff recording is on and the kind is not redacted — see [Manifest diff recording](#manifest-diff-recording) |

## Verb naming

`VerbFor` (`pkg/bastion/audit.go`) names a call by the Kubernetes verb it
performs rather than by its HTTP method, because the same method means very
different things depending on the path:

- A `GET` is `list` when the path addresses a collection and `get` when it
  names an object; a `GET` carrying `?watch=true` (or `watch=1`) is `watch`.
- `POST`/`PUT`/`PATCH`/`DELETE` map onto `create`/`update`/`patch`/`delete`.
- A call whose trailing path segment is a subresource is named after that
  subresource instead of its method — `exec`, `attach`, `portforward`, and a
  `GET` on `pods/log` is `log`. Recording an interactive shell in a
  production pod as a plain "get" would bury the single most sensitive line
  in the trail under the most common one.

## Streaming calls: recorded twice

A long-lived call — `exec`, `attach`, `watch`, `logs -f`, `port-forward` — is
recorded **twice**: once when it opens (`phase: "open"`) and once when it
ends (`phase: "close"`). Without the opening record, a session that runs for
an hour would be invisible in the trail for the whole hour it is running.
The closing record carries the final `status`, `duration_ms`, and both byte
counts — what the cluster sent back and what the user typed or piped in.

## Manifest diff recording

Turning on `record_manifest_diffs` (off by default — see
[Settings](#selective-audit-audit_verbs) below) stores a field-level diff
(`pkg/objdiff`) on the `update` row of a manifest write, computed from the
object before and after the change. Two things are absolute about it:

- It is **never** computed for a **redacted kind** — a Secret. Redaction
  happens on the way a manifest is read *out* of the cluster; a diff over
  the redacted placeholder would either store the placeholder (which says
  nothing) or force decoding the real values just to diff them, which is
  exactly what redaction exists to prevent.
- It is **only ever stored on a successful write** — never for a refused
  write, a guardrail block, or a tunnel failure. The one place that can
  honestly tell a refusal from a success is the call that just made the
  round trip, and `Call` clears the diff before recording anything that is
  not a clean success.

It defaults **off**, unlike every other audit switch: a manifest body can
carry values as sensitive as a Secret's without being a Secret — an inlined
token in a ConfigMap, a Deployment's environment variables — so storing that
extra class of data is something an operator opts into rather than something
that quietly starts happening on upgrade.

## Reading the trail

`GET /api/v1/audit` (`pkg/api/audit.go`) takes:

| Parameter | Meaning |
|---|---|
| `cluster_id`, `user_id` | Exact match |
| `verb` | One value, or repeated/comma-separated for a set (`?verb=create,delete` or `?verb=create&verb=delete`) |
| `status` | One exact HTTP status code |
| `namespace` | Exact match |
| `streaming` | `true` keeps only long-lived calls |
| `failed` | `true` keeps only refusals and errors (a stream's opening record, which carries `101`, is not a failure) |
| `since` / `until` **or** `from` / `to` | RFC3339 timestamps — both pairs are accepted and both are honoured, because a saved link into the trail is something an auditor keeps, and renaming a query parameter to tidy it up would break every bookmark |
| `range` | A fixed preset resolved **server-side**: `15m`, `1h`, `6h`, `24h`, `7d`, `30d`, or `all` (see below) |
| `q` | Free-text match against path, username, resource, namespace |
| `limit` / `offset` | Paging, capped at 100 rows per page |

An explicit `from`/`since` wins over a `range` preset. `range=all` for the
audit trail means "no lower bound at all" — unlike a chart against a metrics
backend, where `all` means the widest window that backend's retention
allows, a trail with no floor is a legitimate thing to ask for.

The presets are a **fixed table resolved on the server**
(`pkg/api/timerange.go`), shared by every ranged surface in the console —
the audit trail, a chart, a link pasted into a ticket. That matters: if the
browser computed "the last hour" against its own clock, three surfaces would
each produce a slightly different window, and a row count that disagrees
with the page it counts is a trail nobody trusts. It also means no caller
can ask for an arbitrary span wide enough to be a table scan dressed as a
filter — the vocabulary the console offers and the vocabulary the API
accepts cannot drift apart.

An **unrecognised verb is dropped rather than refused** — a stale bookmark
naming a verb this build no longer produces narrows to nothing instead of
erroring.

### The narrowing rule

`GET /api/v1/audit` is readable by everyone, but a non-admin is silently
narrowed to their own `user_id` — the handler overwrites `filter.UserID`
with the caller's own id after parsing the query string, so **the query
parameter cannot widen it**. Do not "fix" this by making the whole endpoint
admin-only, and do not let a client-supplied `user_id` override the
narrowing — both would be regressions of a deliberate design.

`GET /api/v1/audit/summary` (the headline totals for the last 24 hours) is
admin-only outright, because the store method behind it has no per-user
filter and the numbers are fleet-wide.

## Selective audit (`audit_verbs`)

On a busy fleet the trail is overwhelmingly `list`/`get`, the rows nobody
reads back. `pkg/auditpolicy` is the mechanism that lets an operator narrow
what a *queryable table* is worth carrying, without ever narrowing what is
observable:

- The decision is a **published immutable snapshot**
  (`auditpolicy.Policy`), resolved from the database by the HTTP layer and
  read lock-free by the gateway's hot path — the hot path must never take a
  database round trip to decide whether to write a row.
- It applies only in `StoreAuditor` (the database sink), never in
  `SlogAuditor` (the structured log): narrowing a queryable table is a
  storage decision, narrowing the log a SIEM already tails would be an
  audit decision, and those are not the same thing.
- **Three things no selection suppresses, ever**: a refusal or an error, any
  streaming call, and kubemg's own `replay`/`recording-get`/
  `recording-delete`/`jit-*` verbs. A control that could hide any of those
  would not be an audit control — it would be a way to act with no trail at
  all.
- **A verb this build does not recognise is always recorded.** An unknown
  verb is the last thing that should start silently dropping out of the
  trail.
- **An empty submitted selection means "record every verb again"**, not
  "record nothing" — the floor above would keep recording refusals and
  sessions regardless, so a server claiming to have gone silent about
  everything else would be lying about itself. This is enforced in
  `pkg/api/settings.go`: sending `audit_verbs: []` clears the override back
  to the default rather than storing an empty set.

The suppressible vocabulary (`auditpolicy.Verbs`) is `get`, `list`, `watch`,
`create`, `update`, `patch`, `delete`, `log`, `exec`, `attach`,
`portforward`. Configure it from Settings → Audit
(`components/settings/AuditSettingsPanel.tsx`), grouped as reads, writes, and
sessions.

## Retention

`audit_retention_days` (1–3650, default 30) governs how long a row survives.
It is an operator setting resolved through the same override-then-default
mechanism as every other runtime setting (`s.settings(ctx)`), so an
out-of-bounds stored value reads as **unset** rather than as whatever it
happens to parse to — a retention window read wrong is a trail deleted.

`startAuditPruner` (`pkg/api/audit_prune.go`) runs every 12 hours, but
**runs once immediately** on boot: a server that has been down for a week
should not wait another twelve hours to honour a retention policy it was
already meant to be enforcing. It **re-reads the window from settings on
every pass** rather than capturing it once at start, so shortening retention
from the Settings page takes effect without a restart. A store failure on
one pass is logged and left for the next — an audit table that is
temporarily oversized is a smaller problem than a background job that gives
up permanently on one transient database error.

The same pass also prunes decided [JIT](../access/jit.md) requests and
[session recordings](session-recording.md) past their own (shorter or equal)
windows — see that page for the recording-specific rule.

`startAuditPolicyRefresher` runs every 30 seconds and republishes the
resolved `audit_verbs`/session-recording snapshot to the in-memory policy —
a save from the Settings page republishes immediately, so this tick exists
purely for the other replica in a multi-instance deployment, whose own
memory knows nothing about a change saved through its sibling.

## Shipping the trail to a SIEM

Two independent paths, and picking one does not disable the other:

- **The structured log.** `SlogAuditor` writes every record — with no
  selection ever applied — as a JSON line to the process's own log stream
  (stderr by default). This is the complete trail, always, regardless of
  what `audit_verbs` narrows the database table to. Point your log
  collector at the container's stdout/stderr.
- **An alarm channel's webhook shape**, if you want specific conditions
  (a denied `delete`, a `CrashLoopBackOff` event) pushed proactively rather
  than pulled by a collector — see [Alarms and integrations](alarms.md),
  particularly the raw webhook payload, which forwards a signal's fields
  unchanged for exactly this purpose.

`MultiAuditor` fans every event out to both the log and the database sink
in one call, so neither depends on the other being configured.
