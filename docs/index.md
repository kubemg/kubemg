# kubemg

**Centralised Kubernetes access, visibility and audit. Clusters dial out; nothing dials in.**

kubemg is a management plane for a fleet of Kubernetes clusters. It gives an
operator one console over every cluster, gives a developer exactly the access
somebody granted them, and keeps a record of every call either of them made —
without opening a single inbound port on any cluster.

<div class="grid cards" markdown>

-   **Install it**

    ---

    Bring the stack up with Docker Compose or on Kubernetes, terminate TLS, and
    work through the environment reference.

    [Quickstart](getting-started/quickstart.md) ·
    [Installation](install/index.md)

-   **Attach a cluster**

    ---

    Register a cluster in agent or direct mode, deploy the agent, and understand
    what differs between the two.

    [Connection modes](clusters/connection-modes.md) ·
    [Deploying the agent](clusters/agent.md)

-   **Grant access**

    ---

    Users, groups, namespace scope, SSO, kubeconfigs, machine accounts and
    just-in-time elevation.

    [The access model](access/model.md) ·
    [Single sign-on](access/sso.md)

-   **Prove what happened**

    ---

    The audit trail, session recordings, and alarms routed to the systems your
    team already watches.

    [Audit trail](audit/trail.md) ·
    [Alarms and integrations](audit/alarms.md)

</div>

## Where to start

If you are **evaluating**, read [What kubemg is](introduction/overview.md) and
[Security model](introduction/security-model.md) — between them they say what
the product does and what it deliberately does not do.

If you are **installing it**, start at the [Quickstart](getting-started/quickstart.md)
to get a stack running on a laptop, then read
[Choosing a deployment](install/index.md) before putting one anywhere real.

If you are **operating an install**, the [Environment reference](install/environment.md),
[Runtime settings](reference/settings.md) and
[Troubleshooting](reference/troubleshooting.md) are the three pages you will keep
open.

## Versions

This manual is versioned against the release tags. The version selector at the
bottom of the sidebar switches between them — an install running 0.6.0 should
read the 0.6.0 pages, because a setting introduced after that tag is not a
setting that install has.

## Licensing

The management plane (server, console, identity and authorisation) is
commercial and closed-source. The in-cluster **agent** is open source and lives
in `agent/` in the repository — see [The agent](reference/agent.md).
