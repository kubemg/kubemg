# Local development

Everything builds and runs in containers. There is no host toolchain to
install — no Go, no Node, no npm. Docker and `make` are the whole list of
prerequisites.

## Bring the stack up

```bash
git clone https://github.com/kubemg/kubemg.git
cd kubemg
make up
```

`make up` builds from source and bind-mounts it, so an edit under `backend/` or
`frontend/` is picked up without a rebuild.

| Service | Address | Notes |
|---|---|---|
| Console | `http://localhost:5173` | Vite dev server, proxies `/api` to the backend |
| Bastion | `https://localhost:8443` | TLS with a self-signed certificate minted on first boot |
| PostgreSQL | `localhost:5432` | database `kubemg`, user `kubemg`, password `kubemg_secret` |

Sign in with **`admin` / `admin`** — the dev stack seeds that account through
`KUBEMG_ADMIN_USERNAME` and `KUBEMG_ADMIN_PASSWORD`.

```bash
make logs    # tail everything
make ps      # what is running
make down    # stop, keep the volumes
make reset   # stop and delete the volumes: data, certificates, recordings
```

!!! warning "TLS is not optional, even locally"
    client-go refuses to send a bearer token over plain HTTP, so a plaintext
    bastion cannot serve a generated kubeconfig or an exec session at all. The
    dev stack mints a self-signed certificate on first boot and keeps it in a
    named volume, so agents that already pinned a copy keep connecting across a
    rebuild. `make reset` throws it away and every existing agent install stops
    trusting the new one.

## Pointing the bastion at a real cluster

`KUBEMG_PUBLIC_URL` is baked into every generated agent install command, so it
has to be an address the *target cluster* can reach. `https://localhost:8443` is
right for a laptop-only stack and wrong the moment an agent tries to dial back.

Put overrides in an untracked `.env` beside `docker-compose.yml` rather than
editing the tracked defaults:

```bash
# .env
KUBEMG_PUBLIC_URL=https://host.docker.internal:8443
KUBEMG_TLS_HOSTS=host.docker.internal,kubemg-backend,backend
KUBEMG_CORS_ORIGINS=http://localhost:5173,http://192.168.1.20:5173
KUBEMG_SESSION_RECORDING_KEY=<openssl rand -base64 32>
```

For a **minikube** cluster on the same host, `https://host.docker.internal:8443`
is the value that works, and the host has to appear in `KUBEMG_TLS_HOSTS` so the
self-signed certificate covers it.

The full list of variables and what each one does is in the user guide's
[Environment reference](../install/environment.md); most of them are boot
defaults an administrator can override at runtime from
[Runtime settings](../reference/settings.md).

## Apple Silicon

The frontend toolchain has to run under emulation:

```bash
DOCKER_DEFAULT_PLATFORM=linux/amd64 make verify
```

`frontend/package-lock.json` carries only `linux-x64` native bindings for oxlint
and rolldown. Under an arm64 `node:22-alpine`, `npm ci` installs no native
binding at all and both tools die with `Cannot find native binding` or
`MODULE_NOT_FOUND`. This is a lockfile fact, not a stale volume — neither
`make reset` nor re-running `npm ci` changes it.

`docker-compose.yml` pins the frontend service to `linux/amd64` for the same
reason. The **backend must not** get the same pin: the Go compiler crashes under
amd64 emulation.

Two more host facts worth knowing before a long debugging session:

- The published agent image has **no arm64 manifest**. Build it locally from
  `agent/Dockerfile` and `minikube image load` it.
- `postgres:16-alpine` sometimes needs a fresh `docker pull` if compose reports
  a stale digest.

## Running a single tool

The `Makefile` wraps every command in a container with cached Go module, Go
build and npm volumes. Prefer a target over inventing an invocation:

```bash
make backend-test      # go test ./...
make backend-vet
make backend-tidy
make agent-test
make frontend-lint     # oxlint
make frontend-test     # vitest
make frontend-build    # tsc -b && vite build
make docs-serve        # this manual at http://localhost:8000, live reload
```

`make help` lists all of them. If you genuinely need a one-off, follow the
Makefile's own pattern rather than reaching for a host toolchain.

## Images

```bash
make image             # management plane, local platform, loaded into docker
make agent-image       # the agent (AGENT_VERSION=x.y.z)
make shell-image       # the browser shell image
make image-check       # prove the amd64 + arm64 matrix still builds
make agent-image-check
```

`make up` is the **dev stack**, not how kubemg runs anywhere real. The
production artefact is the repository-root `Dockerfile`: a node stage builds the
console, the Go stage embeds it into the binary through `backend/pkg/webui`, and
the result is distroless, non-root and about 21 MB against the dev image's
gigabyte. Because the console is served from the binary's own origin, a
production install needs no CORS configuration at all.
