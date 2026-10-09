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

### Something else manages the object

Before a write, the console says when something other than you reconciles the object: an operator, Argo CD, Flux or Helm. The notice appears on the scale and restart panels, while editing the manifest, on a rollback, and in the confirmation for a delete, suspend or resume started from a list, where each affected row is marked. Like the autoscaler notice it never refuses; it tells you the change may not last and where a lasting change belongs.

It is read only from what the reconciler itself writes on the object:

| Managed by | Recognised from | A change made here |
|---|---|---|
| An operator or other controller | a controlling `ownerReference` (a ReplicaSet's Deployment, a custom resource) | **will** be undone, and a deleted object recreated |
| Argo CD | the `argocd.argoproj.io/tracking-id` annotation or `argocd.argoproj.io/instance` label | **may** be reverted: it depends on the application's self-heal setting, which the console does not read |
| Flux Kustomization | `kustomize.toolkit.fluxcd.io/name` | **will** be undone for every field the source sets, unless `kustomize.toolkit.fluxcd.io/reconcile: disabled` is on the object |
| Flux HelmRelease | `helm.toolkit.fluxcd.io/name` | **may** be reverted by drift detection, and is rendered over by the next upgrade |
| Helm | `meta.helm.sh/release-name` | is rendered over by the release's next upgrade |

Some things are deliberately not treated as management:

- `app.kubernetes.io/instance` on its own. Every Helm chart sets it, so it does not mean Argo CD.
- A pod under its ReplicaSet, StatefulSet, DaemonSet or Job, and a Job under its CronJob. Deleting a pod so its controller replaces it is the ordinary case.
- A rollout restart under Argo CD, Flux or Helm. The annotation it writes is not in any manifest those tools apply, so they leave it alone. Only a controlling owner may put the pod template back.

When the cluster has an Argo CD console configured, the object's drawer links to the application, including for applications tracked by annotation and those outside Argo CD's own namespace.

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
