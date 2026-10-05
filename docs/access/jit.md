# Just-in-time access

Request a stronger role on a cluster for a bounded window, approved by someone else. Request it from the cluster you want; decide requests at `/access-requests`, the one page in the Access section that is **not** admin-only.

## Requesting

```
POST /api/v1/jit/requests
{
  "cluster_id": 3,
  "requested_role": "cluster-admin",
  "namespaces": [],
  "duration_minutes": 120,
  "reason": "Rolling restart of the payments deployment after the 14:00 incident; need cluster-admin briefly for the ingress controller."
}
```

| Field | Rule |
| --- | --- |
| `requested_role` | `view`, `edit` or `cluster-admin`. |
| `duration_minutes` | 5 to 1440. The console offers 30, 60, 120, 240 and 480, which the server publishes as `durations`. |
| `reason` | **Mandatory**, at least 10 characters. Stored and shown to the approver in full. |

A request is refused before it is stored when:

- another **pending** request exists for that cluster (one open ask per cluster);
- a **live** elevation already exists for that cluster;
- your **standing** access already covers what you ask for.

`GET /api/v1/jit/requests` returns the requests plus `pending`, `durations`, `statuses`, `roles`, and two booleans for the caller, `can_approve` and `scoped_to_me`. Each request carries `status`, `approver_username`, `approver_comment`, `approved_at`, `expires_at`, `active` and `remaining_seconds`; the last two are resolved server-side with the gateway's own clock, so a countdown cannot disagree with enforcement. A non-admin sees only their own requests, and `user_id` can narrow but never widen that.

## Approval is a two-party act

```
POST /api/v1/jit/requests/:id/approve
{ "comment": "Approved for the incident, please revoke when done." }
```

**A requester cannot approve their own request, whatever their role, a super admin included.** Approving needs an admin who is not the requester and a request still `pending`. Refusals pass the specific rule through:

| Response | Cause |
| --- | --- |
| `403 you cannot approve your own access request` | Self-approval. |
| `403 only an administrator may approve access requests` | Non-admin actor. |
| `409 this request is already <status>` | Already decided. |

`reject` and `revoke` take the same body (the `comment` is optional on all three) and return the same request shape.

Approval writes a **separate grant row** (`source = jit`, with its own expiry), never an edit of the standing one. It is merged by [effective access](model.md#effective-access): standing `view` plus a bounded `cluster-admin` elevation is `cluster-admin` for the window, and the `view` grant is unaffected when it ends.

### What an approver sees

An admin sees every pending request with requester, cluster, role, namespaces, duration and the **full reason**. Approve and Reject act on one request at a time; a live row also offers Revoke. Countdowns come from the server's `remaining_seconds`.

## Expiry is enforced on read

Every access read drops a grant whose window has passed, so a window closes to the second whether or not any background job has run. A sweeper (every 30 seconds) only tidies the request's status to `expired` and reconciles requests whose grant was deleted outside the workflow; it is bookkeeping, not enforcement.

## Reject and revoke are not admin-only

- **Reject** a pending request. An admin may reject anyone's; **the requester may reject their own**, which is how a request is cancelled (there is no separate cancel).
- **Revoke** a live elevation early. An admin may revoke anyone's; **the holder may hand their own back**. Granting needs two people; giving up needs none.

## Statuses and transitions

`approved` and `active` are the same in practice: activation happens in the same transaction as approval. A request is **live** when it is in one of those and its window has not passed.

| Status | Meaning | Reached by |
| --- | --- | --- |
| `pending` | Waiting for a decision. | Creating the request. |
| `approved` / `active` | Carries a live grant. | `approve`, by an admin who is not the requester. |
| `rejected` | Refused (terminal). | `reject`, by an admin or the requester cancelling. |
| `expired` | Ran its window out (terminal). | The sweeper, on `expires_at` passing. |
| `revoked` | Withdrawn early (terminal). | `revoke`, by an admin or the holder; or the sweeper when the grant was deleted outside the workflow. |

Request IDs are random UUIDs, never sequential, because they travel through chat messages and signed tokens.

## The chat callback

```
POST /api/v1/jit/webhooks/callback     (unauthenticated route)
```

A Slack or Teams app carries no kubemg session, so this route needs **all three** of:

1. **A valid Slack request signature** over the raw body (`X-Slack-Request-Timestamp` / `X-Slack-Signature`), checked against every enabled Slack channel's signing secret. No signature headers is an immediate refusal.
2. **A signed, expiring action token** (valid 48 hours) from the original notification. Expired: `403 that approval link has expired; decide it in kubemg instead`. Forged or unparseable: `403 that approval token is not valid`.
3. **An `approver_username` that resolves to an active admin who is not the requester.** Unknown: `403 no kubemg account matches that user; decide this request in kubemg`. Disabled: `403 that account is disabled`.

None is enough alone: the token is visible to everyone in the channel, and the username is only a claim. The self-approval rule applies unchanged.

Slack's own button payload is read directly. Anything else (a Teams flow, a retry) sends JSON:

```json
{
  "token": "THE-SIGNED-ACTION-TOKEN-FROM-THE-MESSAGE",
  "action": "approve",
  "approver_username": "grace",
  "comment": "Approved for the incident, please revoke when done."
}
```

`token` and `approver_username` are required (`400 a signed token and the approver's kubemg username are both required`). `action` is optional; if sent it must match what the token authorises, else `403 that token does not authorise this action`. Success is `200` with a confirmation `text` and the request; the status reads `active`, since approval activates the grant at once.

## Delivery through chat channels

Requests and decisions are announced through the existing Slack/Teams channels (Block Kit with the full reason, or an Adaptive Card), always leading with a console link, which works even if the one-click callback is not wired up. See [Alarms](../audit/alarms.md) for configuring and testing a channel.

## Operator FAQ

**A window expires while a shell is open inside it.** A new call sees the narrower access at once, but a stream already open (such as a `kubectl exec`) keeps running until it ends. In direct mode the cluster-minted token is unaffected and runs to its own expiry. See [the access model's FAQ](model.md#faq).

**An administrator deletes the grant outright while the request is still `active`.** The sweeper closes the request to `revoked` with the comment `grant no longer present; access was revoked outside this request`, so its status catches up with reality. The access itself was gone the moment the row was deleted.
