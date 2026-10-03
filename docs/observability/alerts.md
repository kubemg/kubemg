# Alerts and alarms

kubemg reads a cluster's **Alertmanager** and writes **alarms** into it. The
two halves answer different questions:

- **What is firing?** Every alert the cluster's Alertmanager holds, on a
  cluster-wide **Alerts** page and on each object's drawer, with **Silence**
  beside the ones you may mute.
- **Tell me when this breaks.** From the drawer of a Deployment, pod or volume
  claim you are looking at, **Create alarm** writes a rule for that object to
  the cluster. The cluster's Prometheus evaluates it and its Alertmanager
  routes it.

An alarm is a `PrometheusRule` in the object's namespace, not a record in
kubemg. It keeps firing while kubemg is down. It goes through the silences,
inhibitions and receivers your Alertmanager already has. A GitOps diff shows
it like any other object.

!!! note "Not the same as alarm channels"
    [Alarms and integrations](../audit/alarms.md) are kubemg's own rules about
    cluster Events and the audit trail, delivered from kubemg to a webhook.
    The alarms on this page are rules about an object's health, evaluated by
    the cluster's own Prometheus.

## What the cluster needs

- **The Prometheus operator and kube-state-metrics.** A kube-prometheus-stack
  install provides both. Without the operator, the cluster does not serve
  `PrometheusRule`: the drawer says so and **Create alarm** is refused.
- **An Alertmanager registered as the cluster's alerts datasource.** An
  administrator does this under the cluster's dashboard, **Metrics, logs &
  alerts → Alerts** (see [Datasources](datasources.md#alerts-alertmanager)).
  Creating an alarm requires it, for two reasons. An alarm nobody can see
  firing is half a feature. And the registration carries the **rule labels**
  the cluster's Prometheus loads rules by.
- **Re-applied agent manifests.** An agent's ClusterRoles grant read and write
  on `prometheusrules` from this release on, and only on that resource.
  Prometheus and Alertmanager custom resources are never included, so a
  namespace edit grant cannot redirect pages. Until you re-apply, creating an
  alarm fails with the cluster's own `403`. See
  [Upgrading](../install/upgrading.md#when-agents-must-re-apply-their-manifests).

## Creating an alarm

Open the object in Explore. Its **Overview** has an **Alerts** panel. **Create
alarm** is in that panel and in the drawer's footer. The form offers a fixed
list of conditions for the object's kind:

| Kind | Conditions |
| --- | --- |
| Deployment | Replicas unavailable · Rollout stuck · Pods restarting (threshold) |
| StatefulSet | Replicas not ready · Pods restarting (threshold) |
| DaemonSet | Pods unavailable · Pods misscheduled · Pods restarting (threshold) |
| Pod | Not ready · Crash looping · Pods restarting (threshold) |
| Job | Job failed |
| CronJob | A run failed |
| PersistentVolumeClaim | Volume filling up (percent) · Claim pending |

The form opens on the condition that describes what is wrong with the object
right now. For example, a Deployment whose `Available` condition is false
opens on **Replicas unavailable**. You choose:

- **Threshold.** Only for conditions that have one.
- **How long the condition must hold.** This is the rule's `for`: at once, or
  1 minute up to 1 hour.
- **Severity.** `info`, `warning` or `critical`, the label Alertmanager
  routing usually matches on.
- **Note.** Optional. It is appended to the alert's description.

You never type an expression. kubemg writes the PromQL from the object's
namespace and name, and refuses a name that is not a valid Kubernetes name
rather than quoting it. The rule is named `kubemg-<kind>-<name>-<condition>`,
so a second alarm for the same object and condition is refused. Change the
existing one instead.

Each rule's alert carries `severity`, `namespace`, the object's own
kube-state-metrics label (`deployment`, `pod`, `persistentvolumeclaim`…) and
`kubemg_kind`, `kubemg_name`, `kubemg_condition`, `kubemg_rule`, so an
Alertmanager route can match kubemg's alarms specifically.

A workload's "Pods restarting" condition finds the workload's pods by their
name shape: `<deployment>-<hash>-<suffix>`, `<statefulset>-<ordinal>`. The same
shape is how a Deployment's drawer shows kube-prometheus's own pod alerts for
its pods.

### Who may create one

Anyone whose grant lets them **create `prometheusrules`** in that namespace.
That is the cluster's RBAC answering, through the same impersonated tunnel as
`kubectl`, so in practice it means an `edit` grant. A `view` grant sees the
alarms and is refused the write with the cluster's own message. Changing or
deleting an alarm works the same way. The **Delete** on an alarm only removes
rules kubemg wrote; any other `PrometheusRule` is left to the manifest editor.

## Reading what is firing

**Cluster → Alerts** lists everything the Alertmanager holds that your grant
covers, worst first, with the object each alert is about. It also lists the
alarms kubemg wrote, across your namespaces, and the live silences. An
object's drawer shows the same reading narrowed to that object.

Alertmanager does not know who is asking, so kubemg narrows the answer itself:

- A namespace-scoped grant sees only alerts whose `namespace` label is one of
  its namespaces.
- An alert with no `namespace` label is about the cluster, and only an
  unscoped grant sees it.
- A silence is shown to a scoped grant only when an exact
  `namespace="…"` matcher pins it to one of their namespaces. A regex matcher
  can span namespaces, so it never counts.

## Silencing

**Silence** mutes one firing alert for 1 hour to 7 days, and asks why. The
silence matches **every label of that alert exactly**. kubemg builds it from
the alert as the Alertmanager holds it, so it can be no wider than what you
were looking at. Its author is recorded as `<username> (kubemg)`.

Muting an alert silences pages about that namespace's workloads, so it takes
what changing them takes: an **edit grant over the alert's namespace**. A
cluster-level alert (no namespace) needs an unscoped edit grant.
Administrators may silence anything. **End now** on a silence follows the same
rule.

Both acts are written to the audit trail as `silence-create` and
`silence-expire`, with the namespace and the silence id. A refused attempt is
recorded too.
