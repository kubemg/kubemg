# Quickstart

This gets a working kubemg on your laptop for evaluation, using the dev
Docker Compose stack at the repository root. It is not a production install —
see [Choosing a deployment](../install/index.md) for that.

## Prerequisites

- Docker and `make`. **No Go, Node or npm on the host** — everything builds
  and runs in containers.
- Nothing else. The dev stack brings up its own PostgreSQL 16.

## 1. Clone and bring the stack up

```bash
git clone https://github.com/kubemg/kubemg.git
cd kubemg
cp .env.example .env      # optional: only needed if the defaults are wrong for your machine
make up                   # backend + frontend + PostgreSQL 16
make logs                 # follow the logs
```

`make up` builds the backend and frontend from source and bind-mounts them —
this is the dev stack, not the image a real install runs. `docker-compose.yml`
at the repository root is what it drives.

| Service | Address |
|---|---|
| Console | <http://localhost:5173> |
| API / bastion | `https://localhost:8443` (self-signed certificate, minted at first boot) |
| PostgreSQL | `localhost:5432` |

!!! note "TLS is not optional, even here"
    The dev backend serves HTTPS on `:8443` by default (`KUBEMG_TLS_ENABLED=true`
    in `docker-compose.yml`). `kubectl` and generated kubeconfigs need it:
    client-go refuses to send a bearer token over plain `http://`. See
    [TLS and certificates](../install/tls.md).

## 2. Sign in

The dev compose file seeds a fixed bootstrap administrator explicitly
(`KUBEMG_ADMIN_USERNAME=admin`, `KUBEMG_ADMIN_PASSWORD=admin`), so you can sign
in immediately at <http://localhost:5173> with `admin` / `admin`.

!!! warning "Change it"
    That is a development default seeded only while the users table is empty.
    On a real install you leave `KUBEMG_ADMIN_PASSWORD` unset: the server
    generates a password and prints it once to the log instead
    (`docker compose logs kubemg | grep -A6 'not configured yet'` on the
    production image), and the first-run setup wizard will not let you finish
    without changing it. See [Production checklist](../install/production-checklist.md).

The first sign-in on a fresh database opens a one-time setup wizard —
administrator password, the address clusters will dial, where the agent image
comes from, what the audit trail keeps, and optionally an SSO provider. It
ends on "add your first cluster." It runs exactly once per install; an
existing database never sees it again.

## 3. Point the bastion at an address your cluster can reach

`KUBEMG_PUBLIC_URL` is baked into every generated agent install command, so it
has to be the address the *target cluster* can dial — not the container's own
address, and not `localhost` unless the cluster you are attaching also runs on
this machine. Put it in `.env` next to `docker-compose.yml`:

```bash
KUBEMG_PUBLIC_URL=https://192.0.2.10:8443
KUBEMG_TLS_HOSTS=kubemg-backend,backend,192.0.2.10
KUBEMG_SESSION_RECORDING_KEY=$(openssl rand -base64 32)
```

Then `make down && make up`. It is also editable at runtime from **Settings**
without a restart — the environment variable is only the boot-time default.

## 4. What's next

Attach your first cluster and grant yourself access:

- [Register your first cluster](first-cluster.md)
- [Grant your first access](first-access.md)

For anything beyond a laptop evaluation, move to a real deployment target —
[Choosing a deployment](../install/index.md) covers Docker Compose on a single
VM versus Kubernetes.
