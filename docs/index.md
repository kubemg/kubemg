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

kubemg is **open source in full**. There is no compiled core and no licence key
— the whole tree is readable, buildable and self-hostable. Two licences, split
by directory, because only one half runs inside somebody else's cluster.

| Path | Licence | Why |
|---|---|---|
| Server, console, identity and authorisation | **AGPL-3.0** | This is a product people host for others, so running a *modified* kubemg as a network service means offering that modified source to its users. |
| `agent/`, `deploy/kustomize/` | **Apache-2.0** | The only component that runs **inside a customer's cluster**. A SecOps team has to be able to read it, build it and vendor it into their own tooling without copyleft reaching their infrastructure — see [The agent](reference/agent.md). |

The AGPL does not forbid selling or reselling kubemg; what it forbids is keeping
a modified, network-served fork private. A **commercial licence** is available
alongside it — copyright is held in full by the author — for embedded or OEM
deployments the source-offering obligation does not fit. That does not withdraw
the AGPL grant; it stands for everyone else.

Third-party dependency licences are listed in full in `NOTICE`.
