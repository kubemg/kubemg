# Just-in-time access

A standing grant is the right shape for the access someone needs daily and the wrong shape for what they need twice a quarter. Just-in-time (JIT) access is a request for a stronger role on a cluster, for a bounded window, that somebody else has to approve. Requests are made from the cluster you want, and decided at `/access-requests` — the one page in the Access section that is **not** admin-only.

## Requesting

```
POST /api/v1/jit/requests
```

```json
{
  "cluster_id": 3,
  "requested_role": "cluster-admin",
  "namespaces": [],
  "duration_minutes": 120,
  "reason": "Rolling restart of the payments deployment after the 14:00 incident, need cluster-admin briefly to also touch the ingress controller in kube-system."
}
```

- `requested_role` is one of `view`, `edit`, `cluster-admin`.
- `duration_minutes` must be between 5 and 1440 (24 hours). The console does not offer a free-typed number — it offers a fixed ladder of 30, 60, 120, 240 and 480 minutes, which the server publishes on `GET /api/v1/jit/requests` as `durations`, so the form can never offer a window the server would refuse on submit.
- `reason` is **mandatory** and must be at least 10 characters — a mandatory field people can satisfy with one character is a field that has been switched off by convention. It is stored with the request and shown to the approver.

A request is refused before it is even stored, in three cases: another **pending** request already exists for the same cluster (one open ask per cluster — otherwise the queue fills with duplicates from a double-click and an approver cannot tell which one to act on); a **live** elevation already exists for that cluster (re-requesting would either extend a window an approver already bounded, or replace a role with a weaker one — neither is what anyone means by clicking the button twice); or the requester's **standing** access already covers what is being asked for (asking for what you already have permanently is a no-op that spends an approver's time for nothing).

`GET /api/v1/jit/requests` also carries what the requesting form needs: `durations` (the fixed ladder above), `statuses`, `roles` (`view`, `edit`, `cluster-admin`), and two booleans about the caller — `can_approve` and `scoped_to_me`, so the console knows whether to draw the approvals inbox at all without a second round trip.

```json title="GET /api/v1/jit/requests (excerpt)"
{
  "requests": [
    {
      "id": "3d1f9e2a-...",
      "requester_id": 42,
      "requester_username": "ada",
      "cluster_id": 3,
      "cluster_name": "prod-eu",
      "requested_role": "cluster-admin",
      "namespaces": [],
      "duration_minutes": 120,
      "reason": "Rolling restart of the payments deployment after the 14:00 incident, need cluster-admin briefly to also touch the ingress controller in kube-system.",
      "status": "active",
      "approver_id": 7,
      "approver_username": "grace",
      "approver_comment": "Approved for the incident, please revoke when done.",
      "approved_at": "2026-08-25T14:05:00Z",
      "expires_at": "2026-08-25T16:05:00Z",
      "active": true,
      "remaining_seconds": 3120,
      "created_at": "2026-08-25T14:02:11Z",
      "updated_at": "2026-08-25T14:05:00Z"
    }
  ],
  "pending": 0,
  "durations": [30, 60, 120, 240, 480],
  "statuses": ["pending", "approved", "active", "rejected", "expired", "revoked"],
  "roles": ["view", "edit", "cluster-admin"],
  "can_approve": true,
  "scoped_to_me": false
}
```

`active` and `remaining_seconds` are resolved server-side, against the same clock and the same liveness rule the gateway uses on every call — so a countdown drawn from this response cannot disagree with what the server will actually enforce.

## Approval is a two-party act

```
POST /api/v1/jit/requests/:id/approve
{ "comment": "Approved for the incident, please revoke when done." }
```

```json title="200 OK"
{
  "id": "3d1f9e2a-...",
  "requester_id": 42,
  "requester_username": "ada",
  "cluster_id": 3,
  "cluster_name": "prod-eu",
  "requested_role": "cluster-admin",
  "namespaces": [],
  "duration_minutes": 120,
  "reason": "Rolling restart of the payments deployment after the 14:00 incident, need cluster-admin briefly to also touch the ingress controller in kube-system.",
  "status": "active",
  "approver_id": 7,
  "approver_username": "grace",
  "approver_comment": "Approved for the incident, please revoke when done.",
  "approved_at": "2026-08-25T14:05:00Z",
  "expires_at": "2026-08-25T16:05:00Z",
  "active": true,
  "remaining_seconds": 7200,
  "created_at": "2026-08-25T14:02:11Z",
  "updated_at": "2026-08-25T14:05:00Z"
}
```

`POST /api/v1/jit/requests/:id/reject` and `POST /api/v1/jit/requests/:id/revoke` take the identical body shape (`{"comment": "..."}` — the comment is optional on all three) and return the same `jitRequestResponse` shape, with `status` set to `rejected` or `revoked` respectively.

This is the entire control, and it lives below the HTTP layer specifically so the console and the chat callback cannot disagree about it:

> A requester cannot approve their own request, whatever their role.

A super admin is not exempt — the rule is not about trust in one person, it is about a second person knowing. Approving requires the actor to be an admin, requires the actor's id to differ from the requester's id, and requires the request still be `pending`. A self-approval attempt, an attempt by a non-admin, or an attempt against an already-decided request all answer through `writeJitError`: `403 {"error": "you cannot approve your own access request"}`, `403 {"error": "only an administrator may approve access requests"}`, or `409 {"error": "this request is already <status>"}` respectively — the workflow's own message is passed straight through rather than replaced with a generic refusal, because it names the specific rule that was hit.

**Approval writes a grant that is a separate row, never an edit of the standing one.** `user_cluster_access` rows are unique on `(user_id, cluster_id, source)`; an approval inserts a `source = 'jit'` row with its own `expires_at`, which is merged with any standing grant by [effective access](model.md#effective-access) — the stronger role and unscoped-if-either-is-unscoped rules apply, so a standing `view` plus a bounded `cluster-admin` elevation reads as unscoped `cluster-admin` for the life of the window, and the standing `view` grant is unaffected once it ends.

### What an approver sees

`/access-requests` is open to every signed-in account, not only admins — `scoped_to_me` from the list response is what tells it whether to draw a decision inbox or just "your own requests". For an admin it lists every pending request with the requester's username, the cluster, the requested role and namespaces, the duration, and the **reason in full** — the same text the requester typed, never truncated, because deciding whether to grant `cluster-admin` on production is exactly the moment that sentence matters. Approve and Reject act on one request at a time; a live (`approved`/`active`) row additionally offers Revoke. Countdowns are drawn from the server's own `remaining_seconds` on a slow re-read of the list, rather than a per-second local timer racing the server's clock.

## Expiry is enforced on read

`Store.AccessForUser` filters `expires_at IS NULL OR expires_at > now()` **on every call**, so a JIT window closes to the second regardless of whether any background process has gotten around to it. A separate sweeper (`Engine.RunExpirer`, every 30 seconds) exists only to tidy the `JitRequest` row's own status to `expired` and reconcile requests an administrator's blanket revoke orphaned — it is bookkeeping, not the enforcement mechanism.

## Reject and revoke are not admin-only

- **Reject** (`POST /api/v1/jit/requests/:id/reject`) refuses a still-pending request. An admin may reject anyone's; **the requester may reject their own**, which is how a request is cancelled — it is the same state transition either way, so there is no separate "cancel" endpoint.
- **Revoke** (`POST /api/v1/jit/requests/:id/revoke`) ends a **live** elevation early. An admin may revoke anyone's; **the holder may hand their own back**. Handing privilege back early must never require asking permission — that asymmetry (granting needs two people, giving up needs none) is deliberate.

## Statuses and transitions

`approved` and `active` are treated identically everywhere that matters because activation happens inside the same transaction as the approval — there is no gap between "approved" and "the grant exists." A request is **live** when its status is one of those two **and** its window has not passed, which is the same check the gateway makes on every call.

| Status | Meaning | Reached from | Reached by | Terminal? |
| --- | --- | --- | --- | --- |
| `pending` | Waiting for a decision. | (initial) | `POST /jit/requests` | no |
| `approved` / `active` | Decided and carrying a live grant, counted as live everywhere. | `pending` | `POST .../approve` — an admin who is not the requester | no |
| `rejected` | Refused by an approver. | `pending` | `POST .../reject` — an admin, **or the requester cancelling their own** | yes |
| `expired` | Ran its window out. | `approved`/`active` | `Engine.Sweep`'s expirer pass, on `expires_at` passing — not a person | yes |
| `revoked` | Withdrawn early. | `approved`/`active` | `POST .../revoke` — an admin, **or the holder handing it back**; or `Sweep`'s own reconciliation of an orphaned request (see the FAQ below) | yes |

Reading requests follows the audit trail's rule exactly: `GET /api/v1/jit/requests` narrows a non-admin caller to their own requests regardless of what `user_id` is passed in the query — the parameter cannot widen it, only narrow it further for an admin who wants to filter.

## Request IDs

A request's ID is a random UUIDv4, never a sequence — it travels into a chat message, a signed approval token, and the audit trail, and a guessable id in any of those is an invitation to try the next one along.

## The chat callback

```
POST /api/v1/jit/webhooks/callback     (unauthenticated route — necessarily)
```

A Slack or Teams app carries no kubemg session, so this route sits outside the JWT middleware entirely — and because of that, it authenticates on **three separate things**, and needs all three:

1. **A valid Slack request signature** over the raw body (`X-Slack-Request-Timestamp` / `X-Slack-Signature` headers), checked against every enabled Slack channel's configured signing secret (any one matching is accepted, since a fleet may run several channels and the callback does not say which one it came from; a channel with no signing secret configured is skipped rather than treated as a pass). This is what proves the HTTP call actually came from Slack — and, through Slack, from whoever clicked the button — rather than from anyone who happened to read the notification. No signature headers at all is an immediate refusal.
2. **A signed, expiring action token** (HMAC-SHA256, valid for 48 hours) minted into the original notification. It proves the decision is about a request kubemg itself published and that the link has not gone stale — a request made Friday is still approvable Monday morning, but a token sitting in chat history for months is a standing approval capability nobody meant to leave lying around. An expired token answers `403 {"error": "that approval link has expired; decide it in kubemg instead"}`; a forged or otherwise unparseable one answers the deliberately identical-looking `403 {"error": "that approval token is not valid"}` — which of the two happened is not the caller's business to learn.
3. **An `approver_username` resolving to an active kubemg administrator who is not the requester.** The token alone authorizes an action on a request but names no approver — a chat webhook has no identity of its own — so the audit record for who approved a production elevation would otherwise say "webhook" where a name belongs. An unknown username answers `403 {"error": "no kubemg account matches that user; decide this request in kubemg"}`; a disabled one answers `403 {"error": "that account is disabled"}`.

None of the three is sufficient alone: the token is broadcast to everyone in the channel, so possessing it proves nothing about who clicked; the username is a claim typed into the payload, so it proves nothing about who typed it. Only the Slack signature ties the HTTP request back to Slack itself, and through Slack to whoever actually pressed the button. The self-approval rule applies here unchanged — a signed token naming the requester as approver is still refused.

### The payload shape

The route accepts two shapes, read by `readJitCallback`: Slack's own form-encoded interaction payload (a `payload` form field containing the button press — the approver's Slack username and the button's `value`, which is the signed token, come out of it directly with no `action` field at all, since a Slack button only ever carries one), and kubemg's own plain JSON for anything else (a Teams flow, or a manual retry):

```json title="POST /api/v1/jit/webhooks/callback (non-Slack shape)"
{
  "token": "THE-SIGNED-ACTION-TOKEN-FROM-THE-MESSAGE",
  "action": "approve",
  "approver_username": "grace",
  "comment": "Approved for the incident, please revoke when done."
}
```

`token` and `approver_username` are required — a request missing either answers `400 {"error": "a signed token and the approver's kubemg username are both required"}`. `action` is optional and, when sent, is cross-checked against what the token itself authorizes: a payload claiming `"approve"` against a token signed for `reject` is refused with `403 {"error": "that token does not authorise this action"}`, because the **token**, not the caller-supplied field, is what actually decides which workflow function runs. A successful decision answers `200` with a short confirmation `text` Slack renders into the thread, plus the same `jitRequestResponse` shape every other decision route returns:

```json title="200 OK"
{ "text": "kubemg: access request active by grace", "request": { "id": "3d1f9e2a-...", "status": "active", "...": "..." } }
```

The status in that confirmation text is `active`, not `approved` — `ApproveRequest` activates the grant inside the same transaction as the approval, so there is no intermediate state to report.

## Delivery through chat channels

Requests and decisions are announced through the [alarm dispatcher](../audit/alarms.md#the-dispatcher-never-blocks-and-never-fails-a-caller)'s existing Slack/Teams channel configuration — a new request goes out as Slack Block Kit (with the reason in full) or a Teams Adaptive Card, both leading with a console link, because that is the path that always works regardless of whether the one-click callback is wired up. See [Alarms](../audit/alarms.md) for how a channel is configured and tested.

## Operator FAQ

**A JIT window expires while a shell is still open inside it. What happens to the session?**

The elevation's row disappears from `AccessForUser`'s query the instant `expires_at` passes — no different from any other grant change, because a JIT row is enforced exactly like a standing one (see [Expiry is enforced on read](#expiry-is-enforced-on-read)). What that means for a call already in flight follows the same rule described in [the access model's FAQ](model.md#faq): in **agent mode**, a *new* call sees the narrower access at the next tunnel round trip, but a stream already open and bridging bytes is not reached back into — a `kubectl exec` opened a minute before expiry keeps running as a live socket even though the grant behind it has just narrowed. In **direct mode**, nothing about the cluster-minted token changes at all; it runs out on its own TokenRequest-issued schedule regardless of the JIT row's status. If the requester's standing access is weaker than the elevation was, they are simply back to that standing role for any *new* action the instant the window closes — there is no restore step and no gap, exactly as [Effective access](model.md#effective-access) describes.

**An administrator revokes a user's access outright — not through the JIT workflow, just deleting the grant — while a JIT elevation is still marked `active` for that same user and cluster. What keeps the request row honest?**

`Store.OrphanedJitRequests` finds exactly this case: a `JitRequest` whose status is still in `JitLiveStatuses` (`approved`/`active`) but for which no `user_cluster_access` row with `source = 'jit'` exists any more for that user and cluster — because it was deleted outside the JIT workflow entirely, rather than through `revoke`. `Engine.Sweep`, on its 30-second tick, finds these and closes them out to `revoked` with the comment `"grant no longer present; access was revoked outside this request"`, so the request's own status catches up with reality instead of sitting on `active` forever describing a grant that no longer exists. This is reconciliation, not enforcement — the access itself was already gone the moment the row was deleted, exactly as an expiry is already enforced by `AccessForUser` before the sweeper ever tidies the status. A sweeper pass that fails (a database blip) is logged and left to the next pass rather than treated as fatal, so a transient outage leaves a request row briefly stale rather than crashing anything.
