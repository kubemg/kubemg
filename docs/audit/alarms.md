# Alarms and integrations

Alarms push a message to a webhook when something happens that nobody is watching, such as a pod OOMKilled at 03:00 or a developer refused thirty times in a minute. You set up a **channel** (where to send) and **rules** (what is worth sending). Everything here is admin-only, because a channel is an outbound destination for data that may include audit records.

These rules are kubemg's own, about cluster Events and the audit trail. Rules about an object's health, evaluated by the cluster's Prometheus, are something else: see [Cluster alerts](../observability/alerts.md). To ship the whole audit trail to a SIEM, use a [forwarder](trail.md#forwarding-the-trail), not an alarm.

## Channels and rules

- A **channel** is a destination: a URL, an auth mode and a credential. You configure it once.
- A **rule** is a condition worth sending, pointing at a channel. You tune it often.

They are separate so rules can be edited and toggled without reading a credential back. `GET /api/v1/alarms/channels` returns `has_secret: true/false` instead of the credential, and omitting `secret` on an edit keeps the stored one.

**Deleting a channel deletes its rules**, since a rule with no channel looks like coverage while doing nothing.

## Channel kinds

Set `kind` on `POST`/`PUT /api/v1/alarms/channels`. Each kind sends a different body, and sending one kind's shape to another's endpoint fails silently at the far end. That is why the kind is stored, not inferred from the URL.

| Kind | Sends | Default auth | Notes |
|---|---|---|---|
| `alertmanager` | Alertmanager v2 alert array to `POST /api/v2/alerts` | | Composes with your existing silences, inhibitions and on-call routing. No `endsAt` is sent, because an alarm is a point-in-time fact. Use Alertmanager's `resolve_timeout` to expire it |
| `slack` | Incoming-webhook message with colour-coded attachments | `none` (the URL is the secret) | Also accepted by Slack-compatible endpoints such as Mattermost and Rocket.Chat |
| `teams` | Adaptive Card in a Teams webhook envelope | | Own kind, since Teams rejects Slack's shape with a vague 400. No colour bar, so severity is in the headline |
| `pagerduty` | Events API v2 trigger | `key` | Routing key goes in the body. A channel without one is refused at save. Repeats of the same problem collapse into one incident |
| `servicenow` | Table API incident | `basic` | Both urgency and impact are always sent. `critical` is 1/1, `warning` is urgency 2, impact 3 |
| `webhook` | The raw signal in a thin envelope | | For a SIEM or log aggregator |

Severity is `critical`, `warning` or `info`. Anything else is sent as `info` to PagerDuty.

??? info "Alertmanager payload"
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
    Labels stay low-cardinality. Unbounded values such as a raw path or a message go in annotations.

??? info "PagerDuty payload"
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

??? info "Raw webhook payload"
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

## Rules: the two signals

A rule watches exactly one signal:

- **`cluster_event`**: Kubernetes Events read down the agent tunnel (`OOMKilled`, `FailedScheduling`, anything of `type: Warning`).
- **`audit`**: kubemg's own audit records. Only kubemg can see these. A call it refused never reached the cluster, so there is no Event for it.

Cover both streams with two rules.

### Matchers

Every rule may narrow by `cluster_id` (0 means every cluster, including ones registered later) and a comma-separated `namespaces` list (empty means every namespace). Then:

| Trigger | Matchers |
|---|---|
| `cluster_event` | `event_type` (`Normal` or `Warning`); `event_reasons` (comma-separated, e.g. `OOMKilled,BackOff`) |
| `audit` | `verbs` (comma-separated audit verbs); `denied_only` (only refusals: a 4xx/5xx or a call that never reached the API server); `min_status` (records at or above an HTTP status) |

`agent-displaced` fires whenever a new connection takes over a cluster's agent tunnel. Every agent rollout does this once per cluster, so expect it after each re-apply. Outside a rollout it means someone else holding the registration token has become the agent. The message names the new and previous addresses and versions. `agent-token-rotate` fires on each rotation.

The rule form suggests common reasons (`OOMKilled`, `FailedScheduling`, `BackOff`, `CrashLoopBackOff`, `Failed`, `FailedMount`, `FailedCreatePodSandBox`, `Evicted`, `NodeNotReady`, `Unhealthy`, `FailedAttachVolume`, `ImagePullBackOff`, `ErrImagePull`). It is a suggestion, not an allow-list, so your own CRDs' reasons work.

### Refusals at save time

- A `cluster_event` rule with **neither** an `event_type` nor any `event_reasons` is refused, since it would match every event the cluster emits.
- An `audit` rule naming a verb the trail never records is refused, since it could never fire. Valid verbs are the suppressible verbs, `replay`, `recording-get`, `recording-delete`, `agent-displaced` and `agent-token-rotate`.

## Testing a channel

`POST /api/v1/alarms/channels/:id/test` sends a synthetic alarm immediately, **ignoring the matcher and the cool-off**. A failed test still answers `200` with `{"ok": false, "message": "..."}`, where the message is the endpoint's own words.

## Delivery health, deduplication, and cool-off

Every delivery, real or test, records `last_status` (`ok` or `failed`), `last_message` (up to 500 characters of the endpoint's reply) and `last_attempt_at` on the channel, and Settings shows them per channel. A page that was never sent is otherwise invisible.

- A delivery gets up to 2 attempts, 2 seconds apart.
- Deduplication is per rule **and** per fingerprint, with a cool-off per rule (default 5 minutes). Two rules watching the same event both fire. One rule firing on the same pod every ten seconds does not.
- With no fingerprint from the source, deduplication falls back to the same object and reason.

??? info "How the dispatcher behaves"
    It never blocks and never fails a caller. With no enabled rule, it does nothing per proxied call. Its queue holds 512 signals. A full queue drops the newest signal and logs the first drop, then every thousandth, so a slow webhook never becomes a slow `kubectl`. The closing record of a stream is skipped, so an `exec` does not page twice.

## The cluster-event poller

kubemg polls cluster Events once a minute, only where needed:

- Only clusters that some enabled `cluster_event` rule covers.
- Only clusters with an attached agent. Direct-mode clusters and agent clusters with no live tunnel are skipped.
- **The first pass on a cluster delivers nothing.** It only marks where "now" is, so enabling a rule on an old cluster does not page for history.
- Events older than 15 minutes are dropped, so a skewed clock cannot revive yesterday's incident.

The poller reads as `kubemg:alarm-watcher`, so its reads appear in the audit trail under that name and not under whichever admin configured the rule.

??? info "Exactly one replica polls"
    With several replicas, a lease row in the database lets exactly one poll, so cluster API load does not multiply with replica count. The lease lasts three polling intervals and is renewed every tick. If the holder dies, another replica takes over within about three minutes. If the database errors, no replica polls, which avoids all of them hitting the clusters at once.

## JIT approval notices

`slack` and `teams` channels also carry [just-in-time access](../access/jit.md) notices: a request pending approval, and the decision. These carry buttons (Slack) or an open-in-console action (Teams). Other kinds never receive them, and there is no separate setting. Adding a chat channel counts as the decision to send approvals there.

## Troubleshooting

**A channel shows `last_status: failed`.** Read `last_message` first. It is usually the real reason, such as a 400 naming a missing PagerDuty field or a 401 from a rotated Slack URL. Use Test to iterate.

**A PagerDuty, ServiceNow or Teams channel rejects every payload.** Check the channel's `kind` matches the destination. A Slack-shaped body sent to Teams, or the reverse, fails with a vendor error that rarely says so.

**A rule never fires and delivery health looks fine.** Check that the rule is `enabled`, that its `cluster_id` matches the signal's cluster, and (for `cluster_event`) that the Event's `type` and `reason` match. A `cluster_event` rule on a freshly enabled cluster fires nothing for its first minute by design, so give it two polling intervals.

**Cluster events never trigger an `audit` rule, or the reverse.** Expected. A rule only matches its own signal.

**Alarms went quiet after a scale-out.** The lease holder may have died. Another replica takes over within about three minutes.
