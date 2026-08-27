# Workload actions

Explore offers three write actions beyond the manifest editor: **scale**,
**restart**, and **suspend/resume**. They exist because both operations are
already possible through the YAML editor and are exactly the wrong shape for
it there — scaling a Deployment by hand-editing `spec.replicas` inside a
thousand-line manifest changes one integer at the cost of writing the whole
object, and a rollout restart by hand means knowing that the way to ask for
one is an annotation on the pod template carrying a timestamp nobody reads.

## Which kinds answer for which action

| Kind | Scale | Restart | Suspend | Run now |
|---|---|---|---|---|
| Deployment | yes | yes | — | — |
| StatefulSet | yes | yes | — | — |
| DaemonSet | — | yes | — | — |
| CronJob | — | — | yes | yes |

A DaemonSet has no replica count — it runs one pod per node, and the node
list *is* the count. A CronJob owns Jobs, not pods: there is nothing to scale
and nothing to roll, and deleting it to stop tonight's run would lose the
object, which is why suspend was for a long time its **only** lifecycle
control — see [Running a CronJob now](#running-a-cronjob-now) below.
ReplicaSets answer for scale on the backend and now appear in the inventory
too, but rolling one is still a Deployment's job.

### An autoscaler owns the replica count

If a `HorizontalPodAutoscaler` targets the workload you are scaling, the
scale panel says so **before** you write anything — it reads
`GET .../resources/workload/autoscaler` when it opens and shows the
autoscaler's name and its min/max bounds.

It is a notice, not a refusal. Setting a count by hand under an HPA is a
legitimate thing to do — it is how you force a floor while debugging — and
the manifest editor could always do it anyway. What was missing was being
told: without the notice, the write succeeds, reports the number it set, and
is quietly reverted on the autoscaler's next pass.

## Running a CronJob now

`POST .../resources/cronjob/run` fires a schedule immediately. It is a
**create with no new reach**: kubemg reads the CronJob down the tunnel,
builds a `batch/v1` Job from that object's own `spec.jobTemplate`, and posts
it to the Jobs collection through the same impersonated call every other
write uses — same namespace check, same guardrails, same `create` audit
record. Nothing about the Job's shape comes from the request; the caller
names a CronJob and nothing else.

Two properties are deliberate and worth knowing:

- **The Job is not owned by the CronJob.** `kubectl create job --from=cronjob`
  does the same thing, for the same reason: an owned Job counts against
  `successfulJobsHistoryLimit` and would be reaped out from under whoever
  triggered it. It carries `cronjob.kubernetes.io/instantiate: manual`
  instead, so you can tell a hand-started run from a scheduled one — and it
  is yours to delete when you are done with it.
- **The cluster names it**, via `generateName` derived from the CronJob's
  name (`nightly-report-manual-x7k2p`). A manual run has no natural name, and
  only the cluster can guarantee one is free.

Running is offered on a **suspended** CronJob too: firing one by hand is
exactly what you do while a broken schedule is paused. The schedule itself is
untouched and still fires at its next matching time.

## Read-modify-write, not a patch

Both routes are `POST`s rather than a `PATCH`, and that is a constraint of
the transport, not a style choice: `Proxy.Call` sets `Content-Type:
application/json` on every body it carries down the tunnel, and a JSON merge
patch or a strategic merge patch has to arrive as
`application/json-patch+json` or `application/strategic-merge-patch+json` —
sent as plain `application/json`, the API server answers `415`. So each
action reads the object first, and its `resourceVersion` travels back with
the write, which makes the update **conditional**: if something else changed
the object in between, the write comes back as the API server's own `409
Conflict` rather than silently overwriting a change nobody saw.

- **Scale** goes through the `scale` **subresource**
  (`.../deployments/<name>/scale`), so the body actually written is four
  fields and a number — there is no way for it to disturb a pod template.
  Requests above **1000** replicas are refused before they reach the cluster,
  a check against a mistyped replica count rather than the cluster's own
  quota or scheduler limits, which still apply on top of it.
- **Restart** has no subresource — the annotation *is* the API — so it writes
  the whole object, stamping
  `kubectl.kubernetes.io/restartedAt` (`restartedAtAnnotation`) onto
  `spec.template.metadata.annotations` with the current time. The value is
  never read back; only the fact that it changed matters, since that is what
  changes the pod template hash and makes the controller roll pods. It
  strips `managedFields` on the way out, exactly like the manifest editor,
  and refuses a workload with no pod template rather than inventing one.
- **Suspend/resume** is the same shape as restart — `spec.suspend` is a field
  on the object, so the object is what gets written. A request for the state
  the CronJob is **already** in is answered without a write
  (`suspendState`): asking to suspend six already-suspended schedules out of
  eight selected is two writes and six sentences saying "already suspended",
  not eight writes and six audit records saying nothing happened.

`workloadActions` (backend) and `workloadCapability` (`lib/workloads.ts`,
frontend) are each their own lookup table rather than a flag on the general
resource-kind table, specifically because the three sets — scalable,
restartable, suspendable — do not line up with each other or with the fixed
inventory.

## What the console says happened

Each write answers in the words an operator would use, not the API's:

- Scale: *"`<name>` scaled to 0 replicas — its pods are being removed"*,
  *"scaled to 1 replica"*, or *"scaled to N replicas"*.
- Suspend: *"`<name>` suspended — it will not fire again until it is
  resumed"* / *"resumed"*, or *"`<name>` is already suspended"* when nothing
  was written.
- Run now: *"`<generated name>` started from `<cronjob>`"*, naming the Job the
  cluster created so you can go and watch it.

## Acting over a selection

Every list that carries running things — pods, workloads, jobs, cronjobs —
can turn on a **Select** checkbox column (off by default, so a list read far
more often than it is acted on does not sit one stray click from a
destructive action). Once a selection exists, `bulkActions()` decides what
can be offered: an action is offered only where **every** selected row
answers for it, because a button that silently skips half a selection is
worse than one that is simply absent. Suspend *and* resume can both be
offered over one mixed selection of running and suspended CronJobs — that
is exactly the case an operator reaches for "all of these off", and each row
already in the target state is answered by the server without a write, as
above.

**There is deliberately no bulk API route.** A selection of eight rows is
eight separate calls down the same impersonated tunnel:

- Each is its own act against the cluster and earns its **own line in the
  audit trail** — folding eight deletes into one request would either lose
  that granularity or invent a new audit shape just for this.
- A partial outcome is a real answer a single response would have to invent
  a shape for: *"four deleted, one refused by the cluster's own RBAC, three
  still there"* is exactly what happens when a selection spans objects the
  caller cannot equally reach, and `BulkActionSheet` reports it **per row**.
- The calls are made **sequentially** by the browser, not in parallel — a
  selection is sized to what fits on a screen and was ticked by hand, and the
  thing on the other end is somebody's production API server; racing eight
  writes at it to save two seconds is the wrong trade for a gateway whose
  whole point is being the calm path to the cluster.

Delete is offered on every selection regardless of kind (it is the one
action every object answers for, addressed exactly like the manifest
editor's own delete — see [Exploring resources → Deleting](explore.md#deleting))
and is drawn apart from restart/suspend/resume wherever the sheet lists
actions, since it is the one of the four that cannot be undone by pressing
another button in the same sheet.

## Nothing here is a new permission

Scale, restart, suspend and delete all ride the same impersonated, audited
tunnel every read does. A `view` grant is refused by the cluster's own RBAC
in the cluster's own words — kubemg does not intercept the write to enforce
that itself, the same way a `view` grant does not stop `kubectl scale` from
the command line once impersonation is in effect. What kubemg adds on top is
the namespace scope check every namespaced call gets, the command guardrails,
and the audit record (`VerbFor` records a `POST` to these routes under a
verb that names what actually happened, not just "post").
