# Cluster alerts

This page covers what is firing in a cluster, and how to write a rule that fires when an object breaks. It reads the cluster's **Alertmanager** and writes **alarms** into the cluster.

!!! note "Not the same as alarm channels"
    The alarms here are Prometheus rules about an object's health, evaluated by the cluster's own Prometheus. [Alarms and integrations](../audit/alarms.md) are kubemg's own rules about cluster Events and the audit trail, delivered from kubemg to a webhook.

- **What is firing?** The cluster-wide **Alerts** page and each object's drawer list what Alertmanager holds, with **Silence** beside the alerts you may mute.
- **Tell me when this breaks.** **Create alarm** in an object's drawer writes a rule for it into the cluster.

An alarm is a `PrometheusRule` in the object's namespace, not a record in kubemg. It keeps firing while kubemg is down, goes through your Alertmanager's silences and receivers, and shows up in a GitOps diff like any other object.

## What the cluster needs

- **The Prometheus operator and kube-state-metrics.** kube-prometheus-stack provides both. Without the operator the drawer says so and **Create alarm** is refused.
- **An Alertmanager registered as the alerts datasource.** An administrator adds it under the cluster's dashboard, **Metrics, logs & alerts → Alerts** (see [Datasources](datasources.md#alerts-alertmanager)). It also supplies the **rule labels** the cluster's Prometheus loads rules by.
- **Re-applied agent manifests.** The agent's ClusterRoles grant read and write on `prometheusrules` only. Prometheus and Alertmanager resources are never included, so an edit grant cannot redirect pages. Until you re-apply, creating an alarm fails with the cluster's own `403`. See [Upgrading](../install/upgrading.md#when-agents-must-re-apply-their-manifests).

## Creating an alarm

Open the object in Explore and choose **Create alarm** in the drawer's toolbar. It opens the form in the Overview's **Alerts** panel, below **Dependencies**. Neither the button nor the panel appears until the cluster has an Alertmanager registered and switched on.

| Kind | Conditions |
| --- | --- |
| Deployment | Replicas unavailable · Rollout stuck · Pods restarting (threshold) |
| StatefulSet | Replicas not ready · Pods restarting (threshold) |
| DaemonSet | Pods unavailable · Pods misscheduled · Pods restarting (threshold) |
| Pod | Not ready · Crash looping · Pods restarting (threshold) |
| Job | Job failed |
| CronJob | A run failed |
| PersistentVolumeClaim | Volume filling up (percent) · Claim pending |

The form opens on the condition that matches what is wrong with the object now. You choose:

- **Threshold**, for conditions that have one.
- **How long the condition must hold** (the rule's `for`): at once, or 1 minute to 1 hour.
- **Severity**: `info`, `warning` or `critical`. Alertmanager routing usually matches on it.
- **Note**, optional, appended to the alert's description.

You never type an expression. The rule is named `kubemg-<kind>-<name>-<condition>`, so a second alarm for the same object and condition is refused. Change the existing one instead.

Each alert carries `severity`, `namespace`, the object's own label (`deployment`, `pod`, `persistentvolumeclaim`...) and `kubemg_kind`, `kubemg_name`, `kubemg_condition`, `kubemg_rule`, so a route can match kubemg's alarms specifically.

### Who may create one

Anyone whose grant lets them **create `prometheusrules`** in that namespace. The cluster's RBAC decides, so in practice that means an `edit` grant. A `view` grant sees alarms and is refused the write with the cluster's own message. **Delete** only removes rules kubemg wrote. Any other `PrometheusRule` is left to the manifest editor.

## Reading what is firing

**Cluster → Alerts** lists everything the Alertmanager holds that your grant covers, worst first, with the object each alert is about. It also lists the alarms kubemg wrote and the live silences. An object's drawer shows the same, narrowed to it.

Alertmanager does not know who is asking, so kubemg narrows the answer:

- A namespace-scoped grant sees only alerts whose `namespace` label is one of its namespaces.
- An alert with no `namespace` label is about the cluster, and only an unscoped grant sees it.
- A silence is shown to a scoped grant only when an exact `namespace="..."` matcher pins it to one of their namespaces. A regex matcher never counts.

## Silencing

**Silence** mutes one firing alert for 1 hour to 7 days and asks why. The silence matches **every label of that alert exactly**, so it can be no wider than what you were looking at. Its author is recorded as `<username> (kubemg)`.

Muting needs an **edit grant over the alert's namespace**. A cluster-level alert (no namespace) needs an unscoped edit grant. Administrators may silence anything. **End now** follows the same rule.

Both acts are audited as `silence-create` and `silence-expire`, refused attempts included.
