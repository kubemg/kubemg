# Audit trail

The audit trail records every call kubemg proxies to a cluster, plus kubemg's own sensitive actions. Use it to answer "who did what, to which cluster, and did it work", in the console or in a SIEM.

## What is recorded

- Every call the bastion proxies for a user: list, get, watch, create, update, patch, delete and the streaming verbs.
- kubemg's own sensitive acts: watching or deleting a [session recording](session-recording.md), and every [JIT](../access/jit.md) workflow event (`jit-request`, `jit-approve`, `jit-reject`, `jit-revoke`, `jit-expire`).
- Agent credential events: an administrator rotating a registration token (`agent-token-rotate`), and a new connection taking over a cluster's tunnel (`agent-displaced`, under the user `kubemg:agent`, with both agents' versions, connection times and addresses in the path query). See [The agent](../clusters/agent.md#when-a-connection-displaces-the-agent).

A refusal is recorded like a success: a guardrail block, a namespace-scope violation, a tunnel failure. There is no separate "denied" table. A denial is a row whose `status` is 4xx/5xx or whose `error` is set.

## The record

| Field | Meaning |
|---|---|
| `at` | When the call happened |
| `user_id` / `username` | The kubemg account that made the call |
| `cluster_id` / `cluster` | Which cluster |
| `verb` / `method` | The Kubernetes verb (`list`, `get`, `create`, `exec`, ...) and the raw HTTP method. See [Verb naming](#verb-naming) |
| `path` | The full API path with its query string |
| `namespace` / `resource` | Parsed out of the path when it looks like a Kubernetes API URL |
| `impersonated_user` / `impersonated_groups` | The identity kubemg asserted to the cluster. It ties a kubemg account to the Kubernetes subject that acted |
| `status` | The HTTP status the call ended with |
| `duration_ms` | How long the call took |
| `streaming` / `phase` | Whether the call is long-lived, and whether this row is its open or its close |
| `bytes_out` / `bytes_in` | On a stream's closing record: what came back from the cluster, and what the user sent in |
| `session_id` | Links the two records of one interactive session, and the session's recording |
| `guardrail_policy` / `guardrail_action` | Which [guardrail](../access/guardrails.md) matched and what it did (`block` or `warn`) |
| `source_addr` / `user_agent` | Where the call came from. See [Where a call came from](#where-a-call-came-from) |
| `error` | Set when the call never reached the API server |
| `diff` | Field-level diff of a manifest write. Only when [manifest diff recording](#manifest-diff-recording) is on |

<figure markdown>
  ![One record from the audit trail](../assets/screenshots/audit-trail.png)
  <figcaption>One record from the trail. The identities crossed — the kubemg account and the subject it was impersonated as — are what joins a person to what the cluster saw.</figcaption>
</figure>

## Where a call came from

- `source_addr` is the client address the server resolved, honouring `X-Forwarded-For` and `X-Real-IP` only if the server is configured to trust them (default: none, so behind an untrusted proxy you see the proxy). It carries no port.
- `user_agent` is the client's own claim (`kubectl`, a browser, a CI runner), truncated to 256 characters. It is untrusted, but a credential used by something other than what it was issued to shows up here first.

Both read "not recorded" in the console for calls with no caller (for example the JIT expirer closing a grant) and for rows written before these columns existed. Old rows cannot be backfilled.

## Taking the trail out of the console

`GET /api/v1/audit/export` returns the filtered trail as CSV. See the [REST API reference](../dev/api.md).

- It takes the same parameters as `GET /api/v1/audit`, so an export reproduces the screen. `limit` and `offset` are ignored.
- A non-admin exports only their own rows.
- It stops at 5000 rows, adds a trailing comment row saying so, and sets `X-Kubemg-Export-Truncated`. The console warns about a truncated export instead of reporting success.
- Exporting is not itself audited.

## Verb naming

A call is named by the Kubernetes verb it performs, not its HTTP method.

- `GET` is `list` for a collection, `get` for one object, and `watch` with `?watch=true` (or `watch=1`).
- `POST`/`PUT`/`PATCH`/`DELETE` are `create`/`update`/`patch`/`delete`.
- A subresource names the call instead: `exec`, `attach`, `portforward`, and `GET` on `pods/log` is `log`. A shell in a production pod is never filed as a plain `get`.

## Streaming calls: recorded twice

`exec`, `attach`, `watch`, `logs -f` and `port-forward` are recorded when they open (`phase: "open"`) and when they end (`phase: "close"`). The closing record carries the final `status`, `duration_ms` and both byte counts. Without the opening record, an hour-long session would be invisible until it ended.

## Manifest diff recording

Setting `record_manifest_diffs` (off by default, see [Settings](../reference/settings.md)) stores a field-level diff on the `update` row of a manifest write.

- It is never computed for a Secret.
- It is stored only for a successful write, never for a refusal, a guardrail block or a tunnel failure.

It is off by default because a manifest body can hold values as sensitive as a Secret, such as an inlined token in a ConfigMap or a Deployment's environment variables.

## Reading the trail

`GET /api/v1/audit` takes:

| Parameter | Meaning |
|---|---|
| `cluster_id`, `user_id` | Exact match |
| `verb` | One value, or a set (`?verb=create,delete` or repeated) |
| `status` | One exact HTTP status code |
| `namespace` | Exact match |
| `streaming` | `true` keeps only long-lived calls |
| `failed` | `true` keeps only refusals and errors (a stream's opening `101` is not a failure) |
| `since` / `until` **or** `from` / `to` | RFC 3339 timestamps. Both pairs are accepted, so saved links keep working |
| `range` | Fixed preset resolved on the server: `15m`, `1h`, `6h`, `24h`, `7d`, `30d`, or `all` |
| `q` | Free text against path, username, resource, namespace |
| `limit` / `offset` | Paging, capped at 100 rows per page |

- An explicit `from`/`since` wins over a `range` preset. `range=all` means no lower bound.
- An unrecognised verb is dropped, not refused, so an old bookmark narrows to nothing instead of erroring.

### The narrowing rule

Everyone can read the trail, but a non-admin only ever sees their own rows. The `user_id` parameter cannot widen that. `GET /api/v1/audit/summary` (last-24-hour totals) is admin-only, because its numbers are fleet-wide.

## Selective audit (`audit_verbs`)

On a busy fleet most rows are `list`/`get` that nobody reads. Selective audit narrows what the queryable **table** keeps. Set it at **Admin → Settings → Audit**, where verbs are grouped as reads, writes and sessions.

Suppressible verbs: `get`, `list`, `watch`, `create`, `update`, `patch`, `delete`, `log`, `exec`, `attach`, `portforward`.

- It applies only to the database table, never to the structured log or a forwarder.
- **Never suppressed:** any refusal or error, any streaming call, and kubemg's own `replay`, `recording-get`, `recording-delete`, `jit-*`, `agent-token-rotate` and `agent-displaced`.
- A verb this build does not recognise is always recorded.
- An empty selection means "record every verb again", not "record nothing".

## Retention

`audit_retention_days` (1 to 3650, default 30) sets how long a row survives. An out-of-bounds stored value reads as unset. The same pass also prunes decided [JIT](../access/jit.md) requests and [session recordings](session-recording.md) past their own windows.

Shortening retention takes effect without a restart.

??? info "How pruning runs"
    The pruner runs every 12 hours and once at boot, re-reading the window each pass. A database error on one pass is logged and retried on the next. A 30-second tick republishes the audit-verb and recording selection so replicas pick up a change saved through a sibling.

## Forwarding the trail

The trail already exists in two places: the table the audit page queries, and the server's structured log (a JSON line per record to stderr, always complete, no selection applied). A **forwarder** is the third, and the only one that pushes: it sends the complete trail to a syslog collector. Use it when a SIEM cannot read the container's log stream (Logsign, Splunk, QRadar, anything that speaks syslog). The three paths are independent.

!!! warning "Not an alarm channel"
    An [alarm channel](alarms.md) deduplicates, holds a per-rule cool-off and drops signals when its queue backs up. Each of those loses records. A forwarder applies none of it: every record, every verb, one delivery each. Do not point an unfiltered alarm rule at a SIEM and call it forwarding. To push only specific conditions, use an alarm channel's raw webhook.

Neither the log nor a forwarder applies the `audit_verbs` selection.

### What is sent

RFC 5424 syslog, with one JSON object per record as the message:

```
<134>1 2026-08-25T09:14:02.913Z bastion-0 kubemg - kubemg-audit - {"audit":"kubemg.proxy","timestamp":"2026-08-25T09:14:02.913Z","user_id":7,"username":"ada","cluster_id":4,"cluster":"prod-eu","verb":"delete","method":"DELETE","uri":"/api/v1/namespaces/checkout/pods/checkout-7d9f","namespace":"checkout","resource":"pods","impersonate_user":"kubemg:u:ada","impersonate_groups":"kubemg:edit","status_code":403,"duration_ms":4,"source_addr":"10.4.1.9","user_agent":"kubectl/v1.31.0","error":"refused by guardrail"}
```

The JSON field names are the structured log's, so one parser reads both. **The manifest diff is never forwarded.**

| Header field | Value |
|---|---|
| Priority | `facility × 8 + severity`. Facility is configurable (`local0` to `local7`). Severity is `6` normally and `4` (warning) for a refusal or error |
| Version | `1` |
| Timestamp | The record's own instant, UTC, with nanoseconds |
| Hostname | This process's hostname, or `-` |
| App name | `kubemg` by default. Filter your SIEM rules on it |
| Procid | `-` |
| Msgid | `kubemg-audit` |
| Structured data | `-` |

### Configuring one

**Settings → Audit → Where the trail is shipped**, or the API. Everything is **admin-only, including reading**, because a forwarder sends every username, cluster and namespace to an address someone types in.

| Route | |
|---|---|
| `GET /api/v1/audit/forwarders` | List, with the vocabularies |
| `POST /api/v1/audit/forwarders` | Create |
| `PUT /api/v1/audit/forwarders/:id` | Edit |
| `DELETE /api/v1/audit/forwarders/:id` | Remove |
| `POST /api/v1/audit/forwarders/:id/test` | Deliver one synthetic record |

| Field | |
|---|---|
| `host` | A hostname or IP on its own. A scheme or embedded port is refused by name |
| `port` | Blank takes the default: **515** for `tcp`/`tls`, **514** for `udp` |
| `protocol` | `tcp`, `udp` or `tls` |
| `facility` | `local0` to `local7` |
| `app_name` | Printable ASCII, no spaces. A space is refused, not stripped |
| `octet_counting` | RFC 6587 length-prefix framing instead of a trailing newline. Set it only if the collector expects it |
| `tls_ca_bundle` | PEM, TLS only. A bundle with no certificate is refused rather than falling back to system roots |
| `tls_insecure_skip_verify` | TLS only |
| `enabled` | Off keeps the configuration and stops delivery. Nothing is queued while off |

There is no credential field. Syslog authenticates by network position or TLS, so the row reads back whole, including the CA bundle.

### Which transport

- **`tcp`**: a stream. A record arrives whole or the failure is reported. Prefer it.
- **`tls`**: TCP inside TLS. Use it for anything beyond a datacentre link, since records name people and clusters.
- **`udp`**: fire-and-forget. Supported because many collectors only listen on it. Oversized records are truncated and a dead collector looks the same as a working one. A UDP **Test** only says the address resolved.

**Logsign** ingests syslog on UDP 514 or TCP 515 and parses JSON. Create a JSON-format log source listening on the TCP port, point a `tcp` forwarder at it, and map the field names above.

### Delivery health

Every flush records `last_status`, `last_message` and `last_attempt_at` on the row, and the console shows them in the list. Watch it: a forwarder that stopped working is otherwise invisible. A change in outcome is written at once, an unchanged one at most once a minute.

??? info "What it costs and what it does not guarantee"
    A forwarder never blocks or fails a proxied call. The queue holds 4096 records. Past that, records are **dropped**, logged, and counted on `kubemg_audit_records_dropped_total{sink="forward"}` when [metrics](../install/metrics.md#dropped-audit-records) are enabled. It is not a durable queue: if delivery must survive a collector outage, put a buffer between the two. With no destination configured it costs nothing per call. The destination list is re-read every 30 seconds, and a failed read keeps the previous list.
