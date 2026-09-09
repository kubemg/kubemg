# Audit trail

Every call the bastion proxies to a cluster is recorded, whether the cluster
answered it or kubemg refused it before it ever left the building. The trail
is the single place that answers "who did what, to which cluster, and did it
work" — for a person looking, or for a SIEM tailing the process's own log.

## What is recorded

Two things write to the trail:

- Every call the bastion proxies on a user's behalf — list, get, watch,
  create, update, patch, delete, and the streaming verbs.
- kubemg's own sensitive reads of itself: watching or deleting a session
  recording, and every just-in-time access workflow event
  (`jit-request`/`jit-approve`/`jit-reject`/`jit-revoke`/`jit-expire`). See
  [Session recording](session-recording.md) and [JIT elevated access](../access/jit.md).

A refusal is recorded exactly like a success — a guardrail block, a
namespace-scope violation, a tunnel failure that never reached the API
server. There is no separate "denied" table; a denial is just a row whose
`status` is 4xx/5xx or whose `error` is non-empty.

## The record

A record is deliberately flat rather than nested — a trail is read by a SIEM
far more often than by a person, and nesting buys nothing:

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
| `source_addr` / `user_agent` | Where the call came from, as the server saw it — see [Where a call came from](#where-a-call-came-from) below |
| `error` | Set when the call never reached the API server (a refusal, a tunnel failure) |
| `diff` | The field-level diff of a manifest write, present only when manifest diff recording is on and the kind is not redacted — see [Manifest diff recording](#manifest-diff-recording) |

!!! info "Screenshot pending — `audit-trail.png`"
    The trail filtered to one cluster, with a record's detail sheet open.

## Where a call came from

"From where" is the second question in an access review after "who", and until
these two columns existed the schema had no answer to it at all.

- `source_addr` is the client address the server resolved, through whatever
  proxy headers the engine has been told to trust (`X-Forwarded-For`,
  `X-Real-IP`) — the default being none, so behind an untrusted hop this
  records the hop rather than a header anybody could have written. It never
  carries a port: a source port names a socket that closed seconds later.
- `user_agent` is the client's own claim about what it is — `kubectl`, a
  browser, a CI runner — truncated to 256 characters. It is untrusted by
  construction and worth keeping for exactly that reason: a credential being
  used by something that is not what it was issued to shows up here first.

Both are empty in two ordinary cases, and the console says "not recorded"
rather than drawing a blank:

- a record with no caller — kubemg did it on its own, such as the JIT expirer
  closing out a grant or the alarm poller reading events;
- every row written before the columns existed. **They cannot be backfilled**:
  a call already made has no address left to go and find.

## Taking the trail out of the console

`GET /api/v1/audit/export` answers the query the page is filtered to and
returns it as CSV, so evidence collection is a file rather than a screenshot.

- It takes **exactly the same parameters** as `GET /api/v1/audit` and applies
  the same predicates in the same order, so an export is reproducible from the
  screen it came off. `limit` and `offset` are ignored: paging is the page's
  business, and an offset carried into a file would silently drop the rows
  above it.
- It follows the same narrowing rule as the trail: a non-admin exports their
  own rows, and a `user_id` naming somebody else does not widen that.
- It is bounded at 5000 rows. Past that the file stops, says so in a trailing
  comment row, and sets `X-Kubemg-Export-Truncated` — the console reports a
  truncated export as a warning rather than as a success, because the failure
  mode here is somebody filing a partial file as the whole story.
- It is deliberately **not itself audited**, for the same reason reading the
  trail and reading the recording index are not: it is a read of kubemg's own
  records by somebody already entitled to them.

## Verb naming

A call is named by the Kubernetes verb it performs rather than by its HTTP
method, because the same method means very
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
on the `update` row of a manifest write, computed from the
object before and after the change. Two things are absolute about it:

- It is **never** computed for a **redacted kind** — a Secret. Redaction
  happens on the way a manifest is read *out* of the cluster; a diff over
  the redacted placeholder would either store the placeholder (which says
  nothing) or force decoding the real values just to diff them, which is
  exactly what redaction exists to prevent.
- It is **only ever stored on a successful write** — never for a refused
  write, a guardrail block, or a tunnel failure. The one place that can
  honestly tell a refusal from a success is the call that just made the
  round trip, and the diff is cleared before anything that is not a clean
  success is recorded.

It defaults **off**, unlike every other audit switch: a manifest body can
carry values as sensitive as a Secret's without being a Secret — an inlined
token in a ConfigMap, a Deployment's environment variables — so storing that
extra class of data is something an operator opts into rather than something
that quietly starts happening on upgrade.

## Reading the trail

`GET /api/v1/audit` takes:

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

The presets are a **fixed table resolved on the server**, shared by every
ranged surface in the console —
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
narrowed to their own rows: the caller's own id replaces whatever the query
string asked for, so **the `user_id` parameter cannot widen it**. This is
deliberate rather than an oversight — the trail is readable by everyone
precisely because everyone can only see themselves in it.

`GET /api/v1/audit/summary` (the headline totals for the last 24 hours) is
admin-only outright, because the store method behind it has no per-user
filter and the numbers are fleet-wide.

## Selective audit (`audit_verbs`)

On a busy fleet the trail is overwhelmingly `list`/`get`, the rows nobody
reads back. Selective audit lets an operator narrow what the *queryable
table* is worth carrying, without ever narrowing what is observable:

- It applies **only to the database table**, never to the structured log:
  narrowing a queryable table is a storage decision, narrowing the log a SIEM
  already tails would be an audit decision, and those are not the same
  thing.
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
  everything else would be lying about itself. Submitting an empty selection
  clears the override back to the default rather than storing "nothing".

The suppressible vocabulary is `get`, `list`, `watch`, `create`, `update`,
`patch`, `delete`, `log`, `exec`, `attach`, `portforward`. Configure it at
**Admin → Settings → Audit**, where they are grouped as reads, writes and
sessions.

## Retention

`audit_retention_days` (1–3650, default 30) governs how long a row survives.
It is an operator setting resolved through the same override-then-default
mechanism as every other runtime setting, so an out-of-bounds stored value
reads as **unset** rather than as whatever it happens to parse to — a retention window read wrong is a trail deleted.

The pruner runs every 12 hours, and **runs once immediately** on boot: a server that has been down for a week
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

A separate 30-second tick republishes the resolved audit-verb and
session-recording selection. A save from the Settings page takes effect at
once on the replica that handled it, so this tick exists purely for the other
replicas in a multi-instance deployment, which know nothing about a change
saved through a sibling.

## Shipping the trail to a SIEM

Three independent paths, and picking one does not disable the others:

- **The structured log.** Every record — with no selection ever applied —
  is written as a JSON line to the process's own log stream
  (stderr by default). This is the complete trail, always, regardless of
  what `audit_verbs` narrows the database table to. Point your log
  collector at the container's stdout/stderr.
- **An audit forwarder**, which pushes that same complete trail to a syslog
  collector rather than waiting to be collected — see
  [Forwarding the trail](forwarding.md). This is the path for a SIEM that
  cannot reach into the container's log stream: Logsign, Splunk, QRadar,
  anything that speaks syslog.
- **An alarm channel's webhook shape**, if you want specific conditions
  (a denied `delete`, a `CrashLoopBackOff` event) pushed proactively rather
  than pulled by a collector — see [Alarms and integrations](alarms.md),
  particularly the raw webhook payload, which forwards a signal's fields
  unchanged for exactly this purpose.

An alarm channel is **not** a way to ship the whole trail, and using it as
one produces a SIEM that looks complete and silently is not: the dispatcher
deduplicates by fingerprint, holds a per-rule cool-off, and drops signals
when its queue backs up. Those are the right behaviours for a page and the
wrong ones for a trail. Use a forwarder.

Every event fans out to the log, the table and the forwarder in one pass, so
none of the three depends on another being configured.
