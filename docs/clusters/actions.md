# Workload actions

Explore offers three write actions beyond the manifest editor: **scale**, **restart** and **suspend/resume**, plus **run now** for CronJobs. Use them instead of hand-editing a manifest to change one number or trigger a rollout.

## Which kinds answer for which action

| Kind | Scale | Restart | Suspend | Run now |
|---|---|---|---|---|
| Deployment | yes | yes | — | — |
| StatefulSet | yes | yes | — | — |
| DaemonSet | — | yes | — | — |
| CronJob | — | — | yes | yes |

A DaemonSet has no replica count, and a CronJob owns Jobs rather than pods. ReplicaSets can be scaled but are listed for reading; rolling one is a Deployment's job.

### An autoscaler owns the replica count

If a `HorizontalPodAutoscaler` targets the workload, the scale panel shows its name and min/max bounds **before** you write. It is a notice, not a refusal: setting a count by hand is legitimate, but the autoscaler will revert it on its next pass.

## What each action does

- **Scale** changes only the replica count. Counts above **1000** are refused before reaching the cluster, as a guard against typos; the cluster's own quota still applies.
- **Restart** stamps a `kubectl.kubernetes.io/restartedAt` annotation on the pod template, which makes the controller roll the pods. A workload with no pod template is refused.
- **Suspend/resume** sets the CronJob's `suspend` field. Asking for the state it is already in is answered without a write.
- **Run now** fires the schedule immediately (see below).

Each action reads the object first and sends its version back with the write. If something else changed it in between, you get the cluster's own `409 Conflict` instead of a silent overwrite.

??? info "Why it works this way"
    These are `POST`s, not patches: the tunnel sends bodies as plain `application/json`, which the API server refuses (`415`) for a patch. Scale uses the `scale` subresource, so it cannot disturb the pod template.

The console answers in plain words, for example *"`<name>` scaled to 0 replicas — its pods are being removed"*, *"`<name>` suspended — it will not fire again until it is resumed"*, *"`<name>` is already suspended"*, or *"`<generated name>` started from `<cronjob>`"*.

## Running a CronJob now

**Run now** creates a Job from the CronJob's own job template. It uses the same namespace check, guardrails and `create` audit record as any write, and nothing about the Job comes from the request.

- The Job is **not owned by the CronJob**, as with `kubectl create job --from=cronjob`. It is not reaped by the history limit and is yours to delete. It carries `cronjob.kubernetes.io/instantiate: manual`.
- The cluster names it (`nightly-report-manual-x7k2p`).
- It is offered on a **suspended** CronJob too. The schedule is untouched.

## Acting over a selection

Pods, workloads, jobs and cronjobs can turn on a **Select** checkbox column (off by default). An action is offered only where **every** selected row answers for it. Suspend and resume can both be offered on a mixed selection; rows already in the target state are skipped without a write.

There is **no bulk API route**. A selection of eight is eight sequential calls, each with its own audit record and its own per-row result, so *"four deleted, one refused by RBAC, three still there"* is reported honestly.

**Delete** is offered on every selection and drawn apart from the other actions, since it cannot be undone. See [Browsing resources → Deleting](explore.md#deleting).

## Nothing here is a new permission

Every action goes through the same impersonated, audited tunnel as a read. A `view` grant is refused by the cluster's own RBAC, in the cluster's words. kubemg adds the namespace scope check, the command guardrails and an audit record naming what actually happened.
