# Alarms and integrations

The audit trail answers "what happened" for whoever goes and looks. An
alarm rule is for what nobody is looking at right now: a pod OOMKilled at
03:00, a scheduler that cannot place a workload, a developer whose
`kubectl` was refused thirty times in a minute. All configuration here is
admin-only, because a channel is an outbound destination for records that
may include audit data — adding one is a data-egress decision, not a
preference.

## Channels and rules

Two tables, split the way the responsibility splits:

- **`AlarmChannel`** is a destination — a URL, an auth mode, a credential.
  Configured once.
- **`AlarmRule`** is a condition worth sending, pointing at a channel.
  Configured many times, tuned often.

They are separate rows rather than one so a rule can be edited, duplicated,
and switched on and off by anyone administering alarms without a
credential — a PagerDuty routing key, a bearer token — ever being read back
out to do it. `GET /api/v1/alarms/channels` returns each channel with
`has_secret: true/false` in place of the credential (`AlarmChannel.HasSecret()`);
the actual `Secret` field carries `json:"-"` and never serializes. Omitting
`secret` on a `PUT` keeps the stored one, so changing a channel's URL never
means re-typing its routing key.

**Deleting a channel cascades to its rules.** A rule with no channel to
deliver to would look like coverage while doing nothing, which is worse
than no rule at all.

## Channel kinds

`POST/PUT /api/v1/alarms/channels` with `kind` set to one of six values.
Every kind is a different JSON body over the same webhook POST — an
Alertmanager and a Slack incoming webhook are both "an https endpoint", and
sending one the other's shape fails silently at the far end, which is the
whole reason the kind is stored explicitly rather than inferred from the
URL.

### Alertmanager (`alertmanager`)

Posts the [Alertmanager v2 alert array](https://prometheus.io/docs/alerting/latest/clients/)
to `POST /api/v2/alerts`. This is the kind that *composes*: a fleet that
already routes through Alertmanager gets kubemg's alarms through its
existing silences, inhibitions and on-call rotation rather than beside them.

```json
[
  {
    "labels": {
      "alertname": "kubemgClusterEventOOMKilled",
      "severity": "critical",
      "source": "kubemg",
      "stream": "cluster_event",
      "rule": "OOM in prod",
      "cluster": "prod-eu",
      "namespace": "checkout",
      "reason": "OOMKilled",
      "type": "Warning"
    },
    "annotations": {
      "summary": "[CRITICAL] OOMKilled on pod/checkout-7d9f · prod-eu/checkout",
      "description": "Container checkout exceeded its memory limit",
      "object": "pod/checkout-7d9f"
    },
    "startsAt": "2026-08-25T09:14:02Z",
    "generatorURL": "https://kubemg.example.com/explore/4"
  }
]
```

**There is deliberately no `endsAt`.** An alarm here is a point-in-time
fact — a pod was OOMKilled, a call was refused — not a condition kubemg
continuously evaluates, so there is nothing that would ever tell
Alertmanager the condition has resolved. Sending an `endsAt` kubemg cannot
honour would make the alert vanish from view while the underlying problem
still stands; Alertmanager's own `resolve_timeout` is the correct thing to
expire it instead. Labels stay low-cardinality by design (`alertname`,
`severity`, `source`, `stream`, `rule`, `cluster`, `namespace`, `reason`,
`type`, `verb`, `username`, `status`) — anything unbounded, like a raw path
or a free-text message, goes in `annotations`, because a label with a pod
name in its value set is how a fleet's Alertmanager runs out of memory.

### Slack (`slack`)

Posts an incoming-webhook message with attachments (not Block Kit — see
below for the format Slack channels use *when they carry an approval*).

```json
{
  "text": "[WARNING] BackOff on pod/worker-abc12 · staging/jobs",
  "attachments": [
    {
      "color": "warning",
      "text": "Back-off restarting failed container\n`worker`",
      "fields": [
        { "title": "Cluster", "value": "staging", "short": true },
        { "title": "Namespace", "value": "jobs", "short": true },
        { "title": "Object", "value": "pod/worker-abc12", "short": true },
        { "title": "Reason", "value": "BackOff", "short": true }
      ],
      "footer": "kubemg · Crash loops",
      "ts": 1756112042
    }
  ]
}
```

Attachments are used rather than Block Kit for the plain alarm shape
because attachments carry a colour bar — the fastest way to read severity
in a busy channel at a glance — and they are what every Slack-compatible
endpoint (Mattermost, Rocket.Chat) also accepts. `color` maps `critical` →
`danger`, `warning` → `warning`, `info` → a blue hex. Default auth mode is
`none`: a Slack webhook's own URL *is* its secret.

### Microsoft Teams (`teams`)

Posts an Adaptive Card inside a Teams webhook attachment envelope. It is
its own channel kind — not "Slack-compatible" — because Teams accepts
neither Slack's `attachments` array nor its Block Kit `blocks`; sending
either shape to a Teams webhook fails with a 400 that names nothing useful.

```json
{
  "type": "message",
  "attachments": [
    {
      "contentType": "application/vnd.microsoft.card.adaptive",
      "content": {
        "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
        "type": "AdaptiveCard",
        "version": "1.4",
        "body": [
          { "type": "TextBlock", "text": "[CRITICAL] OOMKilled on pod/checkout-7d9f · prod-eu/checkout", "weight": "Bolder", "size": "Medium", "wrap": true },
          { "type": "TextBlock", "wrap": true, "text": "Container checkout exceeded its memory limit" },
          { "type": "FactSet", "facts": [
            { "title": "Cluster", "value": "prod-eu" },
            { "title": "Namespace", "value": "checkout" },
            { "title": "Reason", "value": "OOMKilled" },
            { "title": "Rule", "value": "OOM in prod" },
            { "title": "Detected", "value": "2026-08-25T09:14:02Z" }
          ] }
        ],
        "actions": [
          { "type": "Action.OpenUrl", "title": "Open in kubemg", "url": "https://kubemg.example.com/explore/4" }
        ]
      }
    }
  ]
}
```

There is no colour bar: an incoming Teams webhook gives an Adaptive Card no
per-message accent to set, so severity is carried in the headline text
instead, which is also what a notification preview shows before the card
itself renders.

### PagerDuty (`pagerduty`)

Posts an [Events API v2](https://developer.pagerduty.com/docs/events-api-v2/trigger-events/)
trigger event.

```json
{
  "routing_key": "R0XXXXXXXXXXXXXXXXXXXXXXXXXXXXX2",
  "event_action": "trigger",
  "dedup_key": "event/prod-eu/checkout/pod/checkout-7d9f/OOMKilled",
  "client": "kubemg",
  "client_url": "https://kubemg.example.com/explore/4",
  "payload": {
    "summary": "[CRITICAL] OOMKilled on pod/checkout-7d9f · prod-eu/checkout",
    "severity": "critical",
    "source": "prod-eu",
    "component": "checkout",
    "group": "prod-eu",
    "class": "OOMKilled",
    "timestamp": "2026-08-25T09:14:02Z",
    "custom_details": {
      "rule": "OOM in prod",
      "stream": "cluster_event",
      "cluster": "prod-eu",
      "namespace": "checkout",
      "object": "pod/checkout-7d9f",
      "reason": "OOMKilled",
      "message": "Container checkout exceeded its memory limit"
    }
  }
}
```

**The routing key rides in the body, not a header** — PagerDuty's Events
API has no header for it, which is why `key` exists as its own auth mode: the
channel's stored secret is written straight into `routing_key`.
**`dedup_key` is the signal's fingerprint**, so repeats of the same underlying problem — a crash loop
re-emitting its event every few seconds — collapse into one PagerDuty
incident instead of opening a new one per occurrence, even across different
kubemg replicas whose in-memory cool-offs know nothing about each other.
Severity maps onto PagerDuty's four words (`critical`/`warning`/`info`, and
`info` is also the fallback for anything else). Creating a PagerDuty
channel without a routing key is refused outright at save time, rather than
left to fail on the first delivery with a 400 nobody is watching for.

### ServiceNow (`servicenow`)

Posts a Table API incident, close enough to what most ITSM tools accept to
serve as the generic ITSM shape.

```json
{
  "short_description": "[CRITICAL] OOMKilled on pod/checkout-7d9f · prod-eu/checkout",
  "description": "[CRITICAL] OOMKilled on pod/checkout-7d9f · prod-eu/checkout\n\nDetected: 2026-08-25T09:14:02Z\nCluster: prod-eu\nNamespace: checkout\nObject: pod/checkout-7d9f\nReason: OOMKilled\nDetail: Container checkout exceeded its memory limit\nRule: OOM in prod\nkubemg: https://kubemg.example.com/explore/4\n",
  "urgency": "1",
  "impact": "1",
  "category": "kubernetes",
  "correlation_id": "event/prod-eu/checkout/pod/checkout-7d9f/OOMKilled",
  "correlation_display": "kubemg",
  "cmdb_ci": "prod-eu"
}
```

**Both `urgency` and `impact` are always sent**, not just one — ServiceNow
computes priority from the pair, and sending only one lands every incident
on the instance's default priority regardless of what actually happened.
The mapping (`serviceNowPriority`) is deliberately not "everything is
critical": a `critical` signal is urgency/impact `1`/`1`, but a `warning` is
urgency `2` with impact `3` — urgent, but narrow — because a single
OOMKilled pod is not a service-wide outage, and filing it as one is how an
ITSM integration earns itself getting switched off. `correlation_id` is the
fingerprint, which is what lets ServiceNow's own correlation rules collapse
a flood of the same underlying event into one ticket rather than four
hundred. Default auth mode is `basic`.

### Raw webhook / SIEM (`webhook`)

Posts the `Signal` struct itself, unreshaped, wrapped in a thin envelope:

```json
{
  "version": "kubemg.alarm/v1",
  "source": "kubemg",
  "rule": "OOM in prod",
  "rule_id": 7,
  "severity": "critical",
  "link": "https://kubemg.example.com/explore/4",
  "signal": {
    "source": "cluster_event",
    "at": "2026-08-25T09:14:02Z",
    "cluster_id": 4,
    "cluster": "prod-eu",
    "namespace": "checkout",
    "reason": "OOMKilled",
    "type": "Warning",
    "object": "pod/checkout-7d9f",
    "message": "Container checkout exceeded its memory limit",
    "fingerprint": "event/prod-eu/checkout/pod/checkout-7d9f/OOMKilled"
  }
}
```

This is the shape a SIEM or log aggregator wants: its own parsers, not a
vendor's alert envelope grafted on top. Nothing here is reshaped from what
the matcher itself reasoned about, so what a SIEM parses is exactly what
fired the rule.

## Rules: the two signals

`AlarmRule.Trigger` is one of two values, and a rule watches exactly one —
matching both would need every field of each, and they mean genuinely
different things:

- **`cluster_event`** — Kubernetes Events read down the agent tunnel
  (`OOMKilled`, `FailedScheduling`, anything of `type: Warning`). This
  matters because the fleet kubemg is built for is exactly the fleet whose
  clusters cannot be scraped from a central Prometheus.
- **`audit`** — kubemg's own audit records. This is the half no
  cluster-side alerting can ever see: a call kubemg refused never reached
  the cluster's API server, so there is no Event for it anywhere but here.

### Matchers

Every rule may narrow by `cluster_id` (0 = every cluster, **including ones
registered later**) and a comma-separated `namespaces` list (empty = every
namespace). Beyond that, the two triggers diverge:

| Trigger | Matchers |
|---|---|
| `cluster_event` | `event_type` (`Normal` or `Warning`); `event_reasons` (comma-separated list, e.g. `OOMKilled,BackOff`) |
| `audit` | `verbs` (comma-separated audit verbs); `denied_only` (keep only refusals — a 4xx/5xx or a call that never reached the API server); `min_status` (keep records at or above an HTTP status) |

`GET /api/v1/alarms/rules` returns `suggested_reasons` — a starter list
(`OOMKilled`, `FailedScheduling`, `BackOff`, `CrashLoopBackOff`, `Failed`,
`FailedMount`, `FailedCreatePodSandBox`, `Evicted`, `NodeNotReady`,
`Unhealthy`, `FailedAttachVolume`, `ImagePullBackOff`, `ErrImagePull`) — as
a suggestion, not an allow-list: an Event reason comes from whichever
controller wrote it, and refusing an unrecognised one would make every
operator's own CRD unalarmable.

### Refusals at save time

Two shapes of rule are rejected on `POST`/`PUT` before they ever reach the
matcher, because both would look configured while doing something nobody
wants:

- A `cluster_event` rule naming **neither** an `event_type` nor at least one
  `event_reasons` entry is refused — such a rule matches every event the
  cluster emits, which is thousands of `Normal` events an hour, and
  discovering that on a pager is a worse way to find out than a 400 at save
  time.
- An `audit` rule naming a verb that is not one the trail actually records —
  every suppressible verb plus the recording-access verbs
  `replay`/`recording-get`/`recording-delete` — is refused, because a rule that can never fire looks identical to one that
  does.

## Testing a channel

`POST /api/v1/alarms/channels/:id/test` sends a synthetic alarm
**synchronously** and **bypasses both the matcher and the cool-off**: an
operator pressing Test is waiting for the answer right there, and a
delivery queued behind the normal pipeline — or one suppressed because a
real alarm on that channel fired four minutes ago — would make the button
useless exactly when it is being used to debug a broken channel. A failed
test still answers `200` with `{"ok": false, "message": "..."}"` — the
request itself succeeded; the answer is "no, the endpoint rejected it,"
with the endpoint's own words.

## Delivery health, deduplication, and cool-off

Every delivery attempt — real or test — records `last_status` (`ok`/
`failed`), `last_message` (up to 500 characters of the endpoint's own
response body), and `last_attempt_at` on the channel row
(`RecordAlarmDelivery`). Nobody notices a page that was never sent, so this
is the only place a silently-broken integration becomes visible; the
Settings UI surfaces it per channel.

A real delivery gets up to 2 attempts, 2 seconds apart (`alarmAttempts`,
`alarmRetryDelay`) — a page is worth one retry; a third attempt against an
endpoint that failed twice in a row would not help, and the health field is
what surfaces the ongoing failure instead.

**Deduplication is per rule *and* per fingerprint**, with a cool-off window
(`CooloffSeconds` on the rule, default 5 minutes) — two rules watching the
same event for different audiences both fire, and the same rule firing on
the same pod every ten seconds does not. When no `Fingerprint` is set by the
producer, `fingerprintOf` falls back to `source/cluster/namespace/object/
reason`, so deduplication degrades to "the same object and reason" rather
than to none at all.

## The dispatcher never blocks, and never fails a caller

Signals arrive from two places: the audit path itself, which feeds every
non-closing audit event into the dispatcher, and the cluster-event poller.
Neither caller has anything useful to do with a delivery failure, so the
dispatcher:

- Returns immediately if no *enabled* rule exists at all (`armed()`) — a
  fleet with no alarms configured pays nothing per proxied call.
- Never blocks: a full internal queue (512 signals) drops the newest signal
  and logs the first drop, then every thousandth — the same trade
  `StoreAuditor` makes for the audit table itself, for the same reason: a
  slow webhook endpoint must never become a slow `kubectl`.
- The closing record of a stream is skipped entirely by `alarmAuditor` — an
  `exec` that opened and later ended is one action, and alarming on both
  the open and the close would page twice for the same shell.

## The cluster-event poller

Cluster Events are not pushed to kubemg; something has to go and read them.
kubemg polls once a minute, and only where it actually needs to:

- **Only clusters some enabled `cluster_event` rule covers** —
  a rule scoped to cluster 4 is not a reason to poll cluster 7.
- **Only where an agent is attached** — a direct-mode cluster, or an agent
  cluster with no live tunnel, is skipped (and its watermark forgotten, so
  it is treated as new if it comes back rather than replaying stale
  history).
- Prefers an already-open cluster-event **watch buffer** shared with the
  events timeline feature when one is warm, and falls back to a plain list
  call when a watch cannot be established on that cluster — alarms must
  never go quiet just because an optimization did not apply.
- **The first pass on a cluster only establishes a watermark; it delivers
  nothing.** Enabling a rule against a cluster that has been running for a
  year must not page for a week of history the moment it is switched on.
  Only events *after* that first pass are offered to the dispatcher.
- Events older than `alarmEventMaxAge` (15 minutes) are dropped even if
  they are technically "new" against the watermark — a cluster with a
  skewed clock, or an event list that arrives out of order, must not
  resurrect yesterday's incident.

The poller impersonates **`kubemg:alarm-watcher`** — a name, not a real
account — and every read it makes down the tunnel is impersonated,
audited, and appears in the trail under that identity exactly as it would
for any other caller, because these reads are kubemg acting on its own
behalf, and attributing them to whichever admin happened to configure the
rule would misrepresent the trail.

### Exactly one replica polls

Polling is the one background job in the product whose cost multiplies
with the replica count and lands on someone else's production API server,
so it is guarded by a **lease** rather than left to run in every process:

- The lease is a single row taken by a **conditional upsert** — `ON
  CONFLICT ... DO UPDATE ... WHERE expires_at < now() OR holder = mine` —
  so two replicas ticking at the same instant is resolved by the database
  itself: exactly one of them sees a row affected.
- It is deliberately **not** a Postgres advisory lock, which would pin a
  pooled connection for the life of a goroutine; a row with an expiry needs
  nothing of the connection pool and is inspectable by an operator who
  wants to know which replica is currently doing the work.
- Renewal is the *same* statement, taken again on every tick — which is why
  the TTL is **3×** the poll interval rather than equal to it: a TTL equal
  to the interval would expire in the gap between two ticks and hand the
  job back and forth between replicas continuously.
- A store error on the lease call means the tick does **not** poll, on any
  replica — every replica sees the same database error, and treating that
  as permission to proceed would put all of them on the cluster at once,
  which is exactly the duplication the lease exists to prevent, arriving
  right when something is already wrong.

## JIT approval notices

Slack and Teams channels also carry [just-in-time access](../access/jit.md)
approval workflow notices — a request pending approval, and the decision
once one is made — through the same dispatcher. These are
questions waiting for a person rather than facts being reported, so their
payloads carry controls: Slack Block Kit buttons, a Teams Adaptive Card with
an `Action.OpenUrl` to the console. Only chat-shaped channels
(`slack`/`teams`) receive them — an Alertmanager or a raw webhook has
nobody reading it who could answer a question, and there is no separate
setting for which channels get approval traffic: adding a Slack channel to
kubemg at all is treated as having already made that egress decision. See
[JIT elevated access](../access/jit.md) for the full approval workflow.

## Troubleshooting

**A channel shows `last_status: failed`.** Read `last_message` first — it
carries up to 512 bytes of the endpoint's own response body, which is
usually the actual reason (a 400 naming a missing PagerDuty field, a 401
from a rotated Slack webhook URL). Use Test to iterate without waiting for
a real signal.

**A rule never fires and delivery health looks fine.** Check the rule was
actually saved as `enabled: true`, that its `cluster_id` (if set) matches
the cluster the signal came from, and — for `cluster_event` — that the
Event's `type`/`reason` actually match what was configured. Remember a
`cluster_event` rule against a freshly-enabled cluster fires nothing on its
first minute by design; give it two polling intervals.

**Cluster events never reach an `audit`-only fleet's rules — that's
expected**, and vice versa: a rule only ever matches its own `Trigger`.
Two rules, one per stream, cover both.

**Alarms went globally quiet after a scale-out.** Check whether the lease
holder died and no replica has taken over yet — `alarmLeaseTTL` bounds this
to at most three polling intervals (3 minutes) before another replica picks
it up automatically; nothing to configure, but worth knowing while waiting.

**A PagerDuty/ServiceNow/Teams channel rejects every payload with a body
you don't recognize.** Confirm the `kind` on the channel actually matches
the destination — sending a Slack-shaped body to a Teams webhook, or vice
versa, is a common misconfiguration and fails with a vendor error that
rarely names the real problem.
