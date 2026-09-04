# Developer guide

This half of the manual is for people who work **on** kubemg: reading the
source, building it, changing it, and sending the change back. If you only want
to attach clusters and run them, everything you need is in the
[User guide](../introduction/overview.md) — nothing here is required to operate
an install.

<div class="grid cards" markdown>

-   **Get a stack running**

    ---

    One `make up`, no host toolchain. Ports, seeded admin, the Apple Silicon
    caveat, and how to point the bastion at a real cluster.

    [Local development](setup.md)

-   **Prove a change**

    ---

    What `make verify` runs, where each kind of test lives, and how to pick the
    cheapest level that can actually answer the question.

    [Building and testing](verify.md)

-   **Understand the shape**

    ---

    Components, trust boundaries, the request path, and the internals of each
    subsystem.

    [System architecture](architecture.md)

-   **Send it back**

    ---

    Branch and pull request conventions, what a review looks for, and the two
    licences a change lands under.

    [Contributing](contributing.md)

</div>

## Repository layout

```
backend/            Go server: Gin + GORM + PostgreSQL 16
  cmd/server/         entrypoint
  pkg/api/            HTTP surface: clusters, IAM, resources, observability, audit
  pkg/bastion/        tunnel listener, kubectl proxy, streaming, audit
  pkg/db/             models and query layer
  pkg/auth/           password hashing, JWT, middleware
  pkg/k8s/            direct-mode clients, TokenRequest, kubeconfig rendering
  pkg/terminal/       session recording (asciinema v2, encrypted at rest)
  pkg/jit/            just-in-time elevation engine
  pkg/guardrails/     command guardrails — the one refusal kubemg makes itself
  pkg/auditpolicy/    which verbs reach the table, and the floor nothing suppresses
  pkg/auditforward/   syslog forwarding of the complete trail
  pkg/observability/  datasources, the server-side query builder, alarms
  pkg/helm/           Helm used as a library, never reimplemented
  pkg/apptemplate/    manifest bundles with declared parameters
  pkg/shell/          the browser shell's pod lifecycle
  pkg/agentpkg/       renders the agent install package (embedded manifests)
  pkg/cache/ certs/ config/ credentials/ cronsched/ objdiff/
  pkg/webui/          the built console, embedded and served on NoRoute
  migrations/         reference DDL only — nothing executes it
frontend/           Vite + React + TypeScript + Tailwind v4
agent/              the in-cluster agent — a separate Go module
deploy/kustomize/   the agent's install manifests (human-facing copy)
deploy/compose/     standalone-VM install — pulls published images, builds nothing
shell/              the browser shell image
docs/               this manual (MkDocs Material)
Dockerfile          the management plane image — spans both Go modules
```

Three layout facts are load-bearing rather than incidental:

- **The agent is its own Go module.** It depends on `gorilla/websocket` and
  nothing else — no client-go — and compiles to about 7 MB. Adding a dependency
  there is a decision, not a detail. See [The agent module](agent.md).
- **The agent manifests exist twice**, `deploy/kustomize/base/` for people to
  read and an embedded copy the server renders install packages from.
  `make manifest-check` fails the build if the two drift, so edit both or
  neither.
- **`backend/migrations/` is reference DDL that nothing executes.** GORM's
  AutoMigrate applies the schema at boot. Where the two disagree, the Go models
  win.

## The two licences

The split is by directory and it is deliberate, so a change has to know which
side of it lands on:

| Path | Licence |
|---|---|
| Server, console, identity and authorisation | **AGPL-3.0** |
| `agent/`, `deploy/kustomize/` | **Apache-2.0** |

The agent and its manifests are the only parts that run inside somebody else's
cluster, and a security team has to be able to read, build and vendor them
without copyleft reaching their infrastructure. Moving code across that boundary
changes its licence, which makes it a review question rather than a refactor.
`NOTICE` is the authority.

## Where the design decisions are recorded

`ARCHITECTURE.md` at the repository root is the design record. For each
subsystem it carries the argument: why it is shaped the way it is, which
alternatives were tried, and what must not be reintroduced. The pages under
[Internals](architecture.md) summarise the shape; the record carries the
reasons. Read the record's section for an area before changing it — a rule
without its reason is easy to "fix" back into a bug.

`roadmap.md` carries open work. A finished item moves to `roadmap-shipped.md`
with a note on what was actually built and what was deliberately rejected. Read
the shipped entry before reimplementing anything it describes.
