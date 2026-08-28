# Docker Compose

`deploy/compose/` is a deployment compose file: it **pulls published images
and builds nothing**, so it runs on a host with no toolchain, no source
checkout, and no route to the internet beyond a registry you control.

!!! note "This is not the dev stack"
    `docker-compose.yml` at the repository root builds from source and
    bind-mounts it — that is what `make up` runs, and it is what
    [Quickstart](../getting-started/quickstart.md) uses. The two are
    unrelated and can coexist on the same machine.

## What it runs

Two containers, and one image your *target clusters* pull — never this host:

| Image | Pulled by | Why |
|---|---|---|
| `ghcr.io/kubemg/kubemg` | this host | The management plane: console and gateway in one binary. |
| `postgres:16-alpine` | this host | Users, grants, clusters, the audit trail. |
| `ghcr.io/kubemg/kubemg-agent` | **your target clusters**, named in `KUBEMG_AGENT_IMAGE` | The outbound tunnel. |

Both kubemg images are published as public manifest indexes covering amd64
and arm64, so pulling them needs no `docker login`.

## Install

```bash
cd deploy/compose
docker compose up -d
docker compose logs kubemg | grep -A6 'not configured yet'
```

That is the whole install. The second command reads the administrator
password, generated on first boot and printed exactly once to the log. Then
open `https://<your-host>:8443` — the browser will warn once, because the
certificate is self-signed on first boot — sign in, and the console walks you
through setup: the address clusters dial, the agent image, what the audit
trail keeps, and optionally an SSO provider, before it lets you register
anything.

Setup will not finish until that generated password is changed. Everything it
collects is stored in the database and editable afterwards from **Settings**.

## Deciding configuration up front instead

Copy `.env.example` to `.env` next to `docker-compose.yml` and set what you
want to decide yourself; anything set there wins over what setup would have
asked for. This is what you want when the install is scripted, when secrets
come from a manager, or when several replicas have to agree on a signing key.

```dotenv
DB_PASSWORD=<generate one — openssl rand -base64 24>
JWT_SECRET=<generate one — openssl rand -base64 48>
KUBEMG_ADMIN_PASSWORD=<optional — otherwise generated and logged>
KUBEMG_PUBLIC_URL=https://kubemg.internal:8443
KUBEMG_TLS_HOSTS=kubemg.internal,192.0.2.10
KUBEMG_SESSION_RECORDING_KEY=<generate one — openssl rand -base64 32>
```

`KUBEMG_PUBLIC_URL` is the one that is easy to get wrong and hard to
diagnose: it is baked into every rendered agent manifest, so `localhost`
produces an agent that dials itself and never connects. Use the LAN, VPN or
DNS name a target cluster can actually resolve and reach, with the port. It
is also the one field setup will not let you past without.

See the [environment reference](environment.md) for every variable this image
reads, and [TLS and certificates](tls.md) for the SSL directory, SANs, and the
agent trust story in detail.

## The volumes, and which to back up

```yaml
volumes:
  - tls-certs:/etc/kubemg/tls          # minted certificate — back this up
  - ./ssl:/etc/kubemg/ssl:ro           # your own certificate, if you supply one
  - session-recordings:/var/lib/kubemg/recordings
```

| Volume | Holds | If you lose it |
|---|---|---|
| `tls-certs` | The certificate minted on first boot | **Every installed agent stops connecting.** It pinned this certificate; a fresh one is a different certificate and the handshake fails. |
| `session-recordings` | Encrypted `.cast.gz` session replays | Audit evidence is gone — recordings are the artefact an auditor asks for. |
| `postgres-data` | Users, grants, clusters, audit trail | The install is gone. |

`./ssl` is a **read-only bind mount**, not a named volume, because it's the
one directory an operator has to be able to drop a file into from the host.
See [TLS and certificates](tls.md) for exact file formats and how the
certificate is picked up.

## Air-gapped installs

Mirror the three images into an internal registry and point the install at
them — nothing else is fetched at runtime; the console's fonts are served
from the binary, not a CDN:

```dotenv
KUBEMG_IMAGE=registry.internal/kubemg/kubemg:0.8.3
KUBEMG_POSTGRES_IMAGE=registry.internal/postgres:16-alpine
KUBEMG_AGENT_IMAGE=registry.internal/kubemg/kubemg-agent:0.8.3
```

`KUBEMG_AGENT_IMAGE` has to be reachable **from your target clusters**, not
from this host — it's written into every rendered agent manifest. A mirror
that requires authentication is not yet supported for the agent: the
rendered manifests carry no `imagePullSecrets`.

## Logs

```bash
docker compose logs -f kubemg
docker compose logs postgres
```

The first-boot administrator password and the "signing sessions with a
server-generated key" / "set JWT_SECRET" notice both land on `kubemg`'s log at
`Info` level. TLS warnings (a plaintext bind refused, a missing recording key)
log at `Warn`.

## Restart and upgrade

```bash
# edit KUBEMG_IMAGE in .env to the new tag
docker compose pull
docker compose up -d
```

Schema migrations run automatically at boot (see [Database](database.md)).
Keep the `tls-certs` volume across the upgrade and the fleet's agents
reconnect on their own without re-installing anything. See
[Upgrading](upgrading.md) for version compatibility between the management
plane and the agent.

A plain `docker compose restart kubemg` is what picks up a certificate you
just dropped into `ssl/` — that directory is read once at boot.

## Backup

Back up, at minimum:

- The `postgres-data` volume (or better, run managed PostgreSQL and back that
  up per your usual process — see [Database](database.md)).
- The `tls-certs` volume, if you are relying on the self-signed certificate
  kubemg minted rather than supplying your own.
- The `session-recordings` volume, and `KUBEMG_SESSION_RECORDING_KEY`
  **kept separately** from that volume's backup — a key stored beside the
  ciphertext it protects defends against nothing.

## Using a real certificate

```bash
cp fullchain.pem deploy/compose/ssl/tls.crt
cp privkey.pem   deploy/compose/ssl/tls.key
chmod 644 deploy/compose/ssl/tls.crt deploy/compose/ssl/tls.key
docker compose restart kubemg
```

`fullchain.pem` + `privkey.pem` are also recognized under those exact names,
so a certbot live directory can be mounted at `/etc/kubemg/ssl` as-is. See
[TLS and certificates](tls.md) for the full detail, including format
conversion and the agent trust story.
