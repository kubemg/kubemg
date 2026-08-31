# Forwarding the trail

The [audit trail](trail.md) already exists in two places: the database
table the audit page queries, and this server's own structured log, which
carries every record with no selection ever applied. A **forwarder** is the
third, and the only one that *pushes*: the complete trail is sent to a
collector rather than waiting for one to come and read it.

That is the case it exists for. A SIEM that can tail a container's log
stream needs nothing here. A SIEM that cannot — an appliance on a segment
that reaches kubemg but not its logging pipeline, or one whose ingest is
syslog and whose operator is not going to run a shipper alongside it — gets
the records sent to it.

## Not an alarm channel

The two look identical from a distance and choosing wrong is expensive, so
this is worth stating plainly.

An [alarm channel](alarms.md) is an **alerting** path. The dispatcher
deduplicates by fingerprint, holds a five-minute per-rule cool-off, and
drops signals when its queue backs up — because a page nobody can read is
worse than a page not sent. Every one of those behaviours loses records.

An audit trail may not lose records. Pointing an alarm rule with no matchers
at a SIEM webhook and calling it "forwarding" produces something that looks
complete, is not, and gives no indication of the difference. A forwarder
applies none of it: every record, every verb, one delivery each.

The verb selection is not applied either. `audit_verbs` narrows what reaches
the queryable **table**, which is a storage decision — a busy fleet's reads
are rows nobody queries. Narrowing what leaves for a SIEM would be an audit
decision, and neither the structured log nor a forwarder makes it.

## What is sent

RFC 5424 syslog, with the message being one JSON object per record:

```
<134>1 2026-08-25T09:14:02.913Z bastion-0 kubemg - kubemg-audit - {"audit":"kubemg.proxy","timestamp":"2026-08-25T09:14:02.913Z","user_id":7,"username":"ada","cluster_id":4,"cluster":"prod-eu","verb":"delete","method":"DELETE","uri":"/api/v1/namespaces/checkout/pods/checkout-7d9f","namespace":"checkout","resource":"pods","impersonate_user":"ada","impersonate_groups":"kubemg:edit","status_code":403,"duration_ms":4,"source_addr":"10.4.1.9","user_agent":"kubectl/v1.31.0","error":"refused by guardrail"}
```

**The JSON field names are the structured log's, exactly.** A parser written
for this server's stdout reads a forwarded record without a second grammar,
which is the point — two shapes for one trail is two things to keep in step
and one place for a detection rule to quietly stop matching.

The header carries what a collector routes on before it parses anything:

| Field | Value |
|---|---|
| Priority | `facility × 8 + severity`. Facility is configurable (`local0`–`local7`); severity is `6` (informational) normally and `4` (warning) for a refusal or an error — the same split `SlogAuditor` logs at, so "filter down to refusals" is one query against either copy |
| Version | `1` |
| Timestamp | the record's own instant, RFC 3339 with nanoseconds, UTC |
| Hostname | this process's hostname, or `-` if it cannot be read |
| App name | configurable, `kubemg` by default. This is what a SIEM rule filters the stream on |
| Procid | `-`, deliberately: a replica id changes on every restart and would only fragment the stream |
| Msgid | `kubemg-audit` |
| Structured data | `-`. Repeating the JSON as SD-ELEMENTs would double the largest thing on the wire and leave no receiver sure which copy wins |

**The manifest diff is not forwarded**, for the same reason the structured
log does not carry it: it is the one audit field that can hold a Secret's
worth of content, it is bounded by nothing useful, and syslog is the
transport least able to survive a large record. It stays in the table, which
is where the [`record_manifest_diffs`](../reference/settings.md) setting
says it goes.

## Configuring one

**Settings → Audit → Where the trail is shipped**, or the API:

| Route | |
|---|---|
| `GET /api/v1/audit/forwarders` | list, with the vocabularies |
| `POST /api/v1/audit/forwarders` | create |
| `PUT /api/v1/audit/forwarders/:id` | edit |
| `DELETE /api/v1/audit/forwarders/:id` | remove |
| `POST /api/v1/audit/forwarders/:id/test` | deliver one synthetic record |

**Administrative throughout, including the read.** A forwarder sends every
record — every username, every cluster, every namespace touched — to an
address somebody types into a box, which makes it a data-egress control
rather than a preference. The set read at once is a map of where this
platform's whole trail goes, which is the same reason the alarm channels are
admin-only.

| Field | |
|---|---|
| `host` | a hostname or IP **on its own**. A scheme or an embedded port is refused by name rather than dialled — it is the mistake an operator makes coming from the alarm channels, where the field is a URL, and a destination that resolves to nothing would sit there failing with an error nobody can explain |
| `port` | blank takes the protocol's default: **515** for `tcp`/`tls`, **514** for `udp`. One default for both would produce a TCP destination pointed at a UDP listener |
| `protocol` | `tcp`, `udp` or `tls` — see below |
| `facility` | `local0`–`local7` (16–23). The named facilities are all spoken for by daemons that predate this by decades |
| `app_name` | the APP-NAME header field. Printable ASCII, no spaces: a space shifts every field after it, so the receiver reads the message as the structured data and the record as garbage. Refused rather than silently stripped, so you see the name you will actually filter on |
| `octet_counting` | RFC 6587 length-prefix framing instead of a trailing newline. Set it only if the collector is configured for it — a receiver expecting one and given the other concatenates every record into a single unparseable line |
| `tls_ca_bundle` | PEM, TLS only. A bundle containing no certificate is **refused** rather than falling back to the system roots: pinning a private CA and silently getting public trust means a forwarder that works right up until it is talking to the wrong collector |
| `tls_insecure_skip_verify` | TLS only, and only stored on a TLS destination |
| `enabled` | off keeps the configuration and stops the delivery. Nothing is queued while it is off |

There is **no credential field**. Syslog authenticates by network position
or by TLS, and a bearer token has nowhere to go in an RFC 5424 frame. That
also means the row reads back whole — unlike an alarm channel there is no
`has_secret` dance, because the CA bundle is a public certificate and an
operator has to be able to check which one they pinned.

### Which transport

- **`tcp`** — a stream. A record arrives whole or the connection fails and
  you are told. Prefer it.
- **`tls`** — TCP inside TLS. What shipping a trail across anything but a
  datacentre link has to be: the records name people, clusters and the
  namespaces they touched.
- **`udp`** — fire-and-forget. Supported because a great many collectors
  only listen on it, but nothing comes back, a record over the datagram
  bound is truncated, and a collector that stopped listening is
  indistinguishable from one that is working.

A UDP **Test** therefore reports what it cannot prove, rather than letting a
green tick be read as proof of delivery: it says the address resolved, and
nothing more.

### Logsign

Logsign's documented ingest is syslog — **UDP 514 or TCP 515** — and its
parsers read JSON, which is why the message is JSON rather than CEF. Create
a JSON-format log source on the Logsign side listening on the TCP port,
point a `tcp` forwarder at it, and the field names above are the ones to map.

## Delivery health, and what it costs

Every flush records its outcome on the row — `last_status`, `last_message`,
`last_attempt_at` — and the console shows it in the list without anything
being opened. This is the field that matters most here: **a forwarder that
stopped working is invisible by construction.** Nothing is missing from
anywhere an operator looks; records simply stop arriving somewhere nobody
watches.

The write is rate-limited to once a minute for an unchanged outcome, and
written immediately whenever the outcome *changes* — "it started failing" is
the whole reason the column exists, and a row update every two seconds for
nine hundred identical successes buys nothing.

`Record` never blocks and never fails a caller. The queue holds 4096
records; past that a record is **dropped** and the drop is logged. That is
the same trade the database sink makes and for the same reason: a slow SIEM
must never become a slow `kubectl`. It also means a forwarder is not a
durable queue — if delivery has to survive an outage of the collector, put
something that buffers between the two.

With no destination configured the hot path does nothing at all: `Record`
returns on an atomic read, so a server that has never been given one pays
nothing per proxied call.

A held connection is kept open across flushes and reopened once on failure.
The destination list is re-read on every save and otherwise every 30
seconds, so a change saved through one replica reaches its siblings; **a
failed read keeps the previous list rather than emptying it**, because a
transient database blip must not silently stop the trail leaving the
platform.
