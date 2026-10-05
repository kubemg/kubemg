# kubemg

**Central, audited access to every Kubernetes cluster. No inbound ports; every call on the record.**

kubemg is a self-hosted access gateway and console for a fleet of Kubernetes
clusters. A small agent in each cluster dials out to kubemg, so no cluster opens
a port. Developers reach clusters through kubemg — from the console or their own
`kubectl` — under their own identity, the cluster's own RBAC decides, and every
call is audited.

<figure markdown>
  ![Explore, showing a cluster's Deployments](assets/screenshots/explore-sidebar.png)
  <figcaption>Explore: live cluster state, read through the agent tunnel under your own identity.</figcaption>
</figure>

## Get going in three steps

1. **Run kubemg** — [Quickstart](getting-started/quickstart.md) on a laptop, or
   [Docker Compose](install/docker-compose.md) / [Kubernetes](install/kubernetes.md)
   for a real install.
2. **Attach a cluster** — register it in the console and run the one
   `kubectl apply` it gives you. See [Adding a cluster](clusters/registering.md).
3. **Give someone access** — grant a role on a cluster, and they get a
   short-lived kubeconfig. See [The access model](access/model.md).

## What you can do with it

<div class="grid cards" markdown>

-   **Work in a cluster**

    ---

    Browse resources, read logs, open a terminal, scale and restart, manage Helm
    releases.

    [Browsing resources](clusters/explore.md) ·
    [Helm](clusters/helm.md)

-   **Control access**

    ---

    Users, groups, SSO, expiring kubeconfigs, just-in-time elevation and
    command guardrails.

    [Users and groups](access/users-and-groups.md) ·
    [Single sign-on](access/sso.md)

-   **Prove what happened**

    ---

    A queryable audit trail, recorded shell sessions, and alarms to the tools
    your team already watches.

    [Audit trail](audit/trail.md) ·
    [Session recording](audit/session-recording.md)

-   **See how it is doing**

    ---

    Live utilisation, node capacity, and metrics and logs from your own
    Prometheus- or Loki-style backend.

    [Datasources](observability/datasources.md) ·
    [Node capacity](clusters/capacity.md)

</div>

## Before production

Read the [Security model](introduction/security-model.md) and the
[Threat model](introduction/threat-model.md): kubemg is the trust anchor for
every cluster it reaches, and those two pages say what that means. Then work
through the [Production checklist](install/production-checklist.md).

## Two guides

This manual has two halves, switched by the buttons at the top of the sidebar.
The **User guide** is for running an install. The **Developer guide** is for
working on kubemg itself — start at [Developer guide](dev/index.md).

The manual is versioned against the release tags; the version selector at the
bottom of the sidebar switches between them.

## Licensing

Open source in full: the server and console are **AGPL-3.0**, and the agent and
its install manifests — the only part that runs inside your cluster — are
**Apache-2.0**. A commercial licence is available for embedded or OEM use.
Third-party licences are listed in `NOTICE`.
