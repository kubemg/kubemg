# What kubemg is

kubemg is a management plane for a fleet of Kubernetes clusters. It gives an
operator one console over every cluster, gives a developer exactly the access
somebody granted them, and keeps a record of every call either of them made —
without opening a single inbound port on any cluster.

## The problem it answers

A team running more than one or two clusters ends up choosing between three
bad options. Hand out a kubeconfig, and it is long-lived, gets copied into
somebody's `~/.kube`, outlives the project it was issued for, and revoking it
means first remembering that it exists. Put a desktop tool like Lens in front
of it, and it is very good at being one person's console but has never heard
of the team — there is nowhere in it to say who may reach production, and no
record afterwards of who did. Install a Rancher-class platform, and it arrives
with controllers and dozens of CRDs and expects to own the cluster once it is
in — its agent dials out too, so the tunnel is not what sets kubemg apart; what
runs at the other end of it is.

kubemg is built around a different trade: **a few megabytes in the cluster,
everything else at the bastion.** The in-cluster piece opens one outbound
connection and holds it; the bastion is where access, audit and observability
actually live.

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

No inbound firewall rule on any cluster. In **agent mode** kubemg stores no
Kubernetes credential — only the registration token the agent presents when
it dials in. That is not the same as holding no power over the cluster: the
agent may impersonate, and forwards what the bastion sends, so **the bastion
plus the tunnel is, in effect, `system:masters` on every agent-mode
cluster** — the trust model every agent-based access product has, stated
rather than implied. [Threat model](threat-model.md) says what that means for
each thing that can leak. See [Connection modes](../clusters/connection-modes.md)
and [How a request flows](../dev/request-flow.md) for the mechanics.

**Postgres** is the one piece of state kubemg itself owns: users, groups,
grants, cluster registrations, settings, audit records and session-recording
metadata. It holds no Kubernetes credential in agent mode, and in direct mode
holds exactly the service account token an administrator registered — but in
either mode it holds the key that signs every session, which is why stored
credentials are encrypted under `KUBEMG_SECRET_KEY`. See
[Credentials encrypted at rest](../install/database.md#credentials-encrypted-at-rest).

## What a developer sees

A developer's rail is two sections with no dead ends: **Operate** (the
cluster they're in, and the fleet it belongs to — fleet overview, Explore,
terminal, logs, scale/restart, metrics) and **Activity** (their own access
requests, their own audit trail, their own session recordings). Everything in
both resolves for everyone; there is nothing to be an administrator to reach a
row that isn't there. A cluster's dashboard for a non-admin is a slim identity
card plus the counts and alerts drawn from the resource lists they can already
read — see [Browsing resources](../clusters/explore.md).

!!! info "Screenshot pending — `fleet-overview.png`"
    The fleet overview: the connection chain — cluster, tunnel, kubemg, proxy,
    you — as the page's own heading, one row per cluster, followed by the
    table banded by environment with the queue at the top naming what is
    waiting for an administrator.

## What an administrator sees

Everything a developer sees, plus **Admin**: cluster registration and
inventory, users, groups, the permission matrix, SSO federation, guardrail
rules, alarm routing, and runtime settings. The Admin section is gated once,
at the rail, and disappears whole for anyone who isn't one — a developer's
navigation does not hint at sections it cannot open.

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
| Audit forwarding | Pushes the complete trail to a syslog collector as RFC 5424 with a JSON message, for a SIEM that cannot tail the container's log stream | [Forwarding the trail](../audit/forwarding.md) |

Continue to [How a request flows](../dev/request-flow.md) for the mechanics behind
all of this, or [Security model](security-model.md) for what is and is not
trusted where.
