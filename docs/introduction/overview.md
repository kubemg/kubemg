# What kubemg is

kubemg is a management plane for a fleet of Kubernetes clusters: one console over every cluster, exactly the access somebody granted each developer, and a record of every call, with no inbound port opened on any cluster. This page says what it is, what it is not, and what each kind of user sees.

## The problem it answers

Long-lived kubeconfigs get copied around and are hard to revoke. Desktop tools serve one person, not a team: no record of who reached production. Rancher-class platforms install controllers and CRDs and expect to own the cluster.

kubemg's trade is **a few megabytes in the cluster, everything else at the bastion.** The in-cluster agent opens one outbound connection and holds it; access, audit and observability live at the bastion.

## What it is, and what it deliberately is not

kubemg is a **bastion/gateway** that proxies Kubernetes API traffic under
impersonated identities, plus the console, identity and audit surfaces built
on top of that proxy.

It is **not**:

- A CI/CD system. It does not build, test or deploy anything — it is the
  access layer a pipeline or a person goes through to reach a cluster. See
  [Machine accounts](../access/machine-accounts.md) for the pipeline case.
- A monitoring stack. It reads live utilisation from a cluster's own Metrics
  API and history from a datasource the cluster already has (Prometheus,
  VictoriaMetrics, Loki, and friends) — it does not collect, store or scrape
  metrics itself. See [Datasources](../observability/datasources.md).
- An in-cluster controller platform. The agent installs no CRDs, runs no
  controllers, and caches no cluster state. Its own ClusterRole grants exactly
  one privilege: impersonation.

## Component map

```text
                    developer's kubectl        browser (console)
                            |                          |
                            |  HTTPS :443              |  HTTPS :443
                            v                          v
                +---------------------------------------------------+
                |                    kubemg bastion                 |
                |                                                    |
                |  console/API  --  gateway proxy  --  audit/record  |
                |       ^               |                            |
                |       |               v                            |
                |   PostgreSQL     tunnel listener                   |
                |   (users, grants,    ^                             |
                |    audit, settings)  |                             |
                +-----------------------|---------------------------+
                                        | outbound WebSocket,
                                        | opened BY the cluster
                                        |
                +-----------------------|---------------------------+
                |  target cluster        v                          |
                |               kubemg-agent (~7 MB)                |
                |               (open source, no CRDs)               |
                |                        |                          |
                |                        v  Impersonate-User/-Group  |
                |                 kube-apiserver                     |
                |                 (RBAC decides)                     |
                +----------------------------------------------------+
```

No inbound firewall rule on any cluster. In **agent mode** kubemg stores no Kubernetes credential, only the registration token the agent presents.

!!! warning "The bastion plus the tunnel is `system:masters`"
    The agent may impersonate, so the bastion plus the tunnel is in effect `system:masters` on every agent-mode cluster. [Threat model](threat-model.md) covers what that means for each thing that can leak.

See [Connection modes](../clusters/connection-modes.md) and [How a request flows](../dev/request-flow.md).

**Postgres** holds the state kubemg owns: users, groups, grants, cluster registrations, settings, audit records and recording metadata. It holds the key that signs every session, so stored credentials are encrypted under `KUBEMG_SECRET_KEY`. See [Credentials encrypted at rest](../install/database.md#credentials-encrypted-at-rest).

## What it looks like

<figure markdown>
  ![Fleet overview](../assets/screenshots/fleet-overview.png)
  <figcaption>The fleet overview: what needs an administrator, capacity, and how each cluster is linked.</figcaption>
</figure>

<figure markdown>
  ![Explore sidebar](../assets/screenshots/explore-sidebar.png)
  <figcaption>Explore: a resource browser over live cluster state.</figcaption>
</figure>

<figure markdown>
  ![Audit trail](../assets/screenshots/audit-trail.png)
  <figcaption>The audit trail: every call, filterable.</figcaption>
</figure>

<figure markdown>
  ![Users table](../assets/screenshots/users-table.png)
  <figcaption>Users, with their groups and grants.</figcaption>
</figure>

## What a developer sees

The rail has two sections: **Operate** (fleet overview, Explore, terminal, logs, scale/restart, metrics) and **Activity** (their own access requests, audit trail and session recordings). A non-admin's cluster dashboard is an identity card plus counts and alerts drawn from the resource lists they can already read; see [Browsing resources](../clusters/explore.md). Their fleet page shows only their own requests waiting and kubeconfigs about to expire.

## What an administrator sees

Everything a developer sees, plus **Admin**: cluster registration, users, groups, the permission matrix, SSO, guardrail rules, alarm routing and settings. The section disappears whole for non-admins.

The fleet page opens on four figures, each a link onto the rows it counts:

| Figure | Opens |
| --- | --- |
| Requests waiting | The access-request queue |
| Refused · 24h | The audit trail, narrowed to refused or failed calls over 24 hours |
| Kubeconfigs expiring · 24h | The credentials register, narrowed to those running out within a day |
| Agents behind | The clusters table, narrowed to agents older than the newest in the fleet |

A figure kubemg could not read shows a dash, never a zero. Counts are read when the page opens. A banner above lists what waits on an administrator (a closed tunnel, a cluster that never dialled in, a request to approve, an agent behind), or says how many clusters are linked. **Fleet capacity**, **Links across the fleet** and **Recent activity** sit beside the clusters table.

## Feature tour

| Area | What it does | Read more |
| --- | --- | --- |
| Fleet | Environment-banded cluster cards, an inventory table, a five-step registration wizard | [Adding a cluster](../clusters/registering.md) |
| Explore | A resource browser over live cluster state, including CRDs a cluster actually serves | [Browsing resources](../clusters/explore.md) |
| Operate | Scale, restart, YAML edit, `port-forward`, Helm values, in-browser terminal and pooled logs | [Workload actions](../clusters/actions.md), [Terminals and logs](../clusters/terminals-and-logs.md) |
| Observability | Live utilisation plus history from a registered datasource, without the browser ever sending a query | [Metrics and logs](../observability/metrics-and-logs.md) |
| Access | Users, groups, effective-permission merging, SSO federation, machine accounts, just-in-time elevation | [The access model](../access/model.md) |
| Guardrails | Refuses destructive commands on kubemg's own authority, including inside an interactive shell | [Command guardrails](../access/guardrails.md) |
| Audit | A queryable trail with session replay, and a recordings index | [Audit trail](../audit/trail.md), [Session recording](../audit/session-recording.md) |
| Alarms | Routes cluster events and kubemg's own audit records to Alertmanager, Slack, Teams, PagerDuty, ServiceNow or a SIEM webhook | [Alarms and integrations](../audit/alarms.md) |
| Audit forwarding | Pushes the complete trail to a syslog collector as RFC 5424 with a JSON message, for a SIEM that cannot tail the container's log stream | [Forwarding the trail](../audit/trail.md#forwarding-the-trail) |

Continue to [How a request flows](../dev/request-flow.md) for the mechanics behind
all of this, or [Security model](security-model.md) for what is and is not
trusted where.
