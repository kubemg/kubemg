# Docker Compose

Run the management plane on one host from published images, with no toolchain,
no source checkout and no internet beyond a registry you control. Start the
stack, read the generated admin password, and finish setup in the console.

!!! note "This is not the dev stack"
    `docker-compose.yml` at the repository root builds from source; that is
    `make up`, used by the [Quickstart](../getting-started/quickstart.md). The
    two are unrelated and can coexist.

## What it runs

| Image | Pulled by | Why |
|---|---|---|
| `ghcr.io/kubemg/kubemg` | this host | Console and gateway in one binary. |
| `postgres:16-alpine` | this host | Users, grants, clusters, the audit trail. |
| `ghcr.io/kubemg/kubemg-agent` | **your target clusters**, named in `KUBEMG_AGENT_IMAGE` | The outbound tunnel. |

The kubemg images cover amd64 and arm64 and need no `docker login`.

## Install

```bash
cd deploy/compose
docker compose up -d
docker compose logs kubemg | grep -A6 'not configured yet'
```

The second command prints the administrator password, generated on first boot
and shown once. Open `https://<your-host>:8443` (the browser warns once: the
certificate is self-signed), sign in, and setup collects the address clusters
dial, the agent image, audit retention and optionally SSO. Setup will not
finish until the generated password is changed. All of it is editable later in
**Settings**.

## Deciding configuration up front instead

For scripted installs, secrets from a manager, or agreeing on a signing key,
copy `.env.example` to `.env`. Anything set there wins over setup.

```dotenv
DB_PASSWORD=<generate one — openssl rand -base64 24>
JWT_SECRET=<generate one — openssl rand -base64 48>
KUBEMG_ADMIN_PASSWORD=<optional — otherwise generated and logged>
KUBEMG_PUBLIC_URL=https://kubemg.internal:8443
KUBEMG_TLS_HOSTS=kubemg.internal,192.0.2.10
KUBEMG_SESSION_RECORDING_KEY=<generate one — openssl rand -base64 32>
KUBEMG_SECRET_KEY=<generate another — openssl rand -base64 32>
```

`KUBEMG_PUBLIC_URL` is the easy one to get wrong: it is baked into every agent
manifest, so `localhost` makes an agent that dials itself. Use a name a target
cluster can resolve and reach, with the port. Setup will not let you past
without it.

See the [environment reference](environment.md) for every variable and
[TLS and certificates](tls.md) for the SSL directory, SANs and agent trust.

## The volumes, and which to back up

| Volume | Holds | If you lose it |
|---|---|---|
| `tls-certs` | Working copy of the minted certificate | Nothing: the next boot restores it from the database, see [TLS](tls.md#the-minted-certificate-is-kept-in-the-database-too). |
| `session-recordings` | Encrypted `.cast.gz` session replays | Audit evidence is gone. |
| `postgres-data` | Users, grants, clusters, audit trail, the minted certificate | The install is gone and every installed agent stops connecting (it pinned that certificate). |

`./ssl` is a read-only bind mount (not a named volume) so you can drop your own
certificate into it from the host. See [TLS and certificates](tls.md).

## Air-gapped installs

Mirror the three images and point `.env` at them:

```dotenv
KUBEMG_IMAGE=registry.internal/kubemg/kubemg:0.14.0
KUBEMG_POSTGRES_IMAGE=registry.internal/postgres:16-alpine
KUBEMG_AGENT_IMAGE=registry.internal/kubemg/kubemg-agent:0.14.0
```

The agent image must be reachable from your **target clusters**. For
authenticated mirrors (`KUBEMG_AGENT_IMAGE_PULL_SECRET`) and carrying images
across on media, see [Air-gapped installs](air-gapped.md).

## Logs

```bash
docker compose logs -f kubemg
docker compose logs postgres
```

The first-boot admin password and the signing-key notice log at `Info`; TLS
warnings (plaintext bind refused, missing recording key) at `Warn`.

## Restart and upgrade

```bash
# edit KUBEMG_IMAGE in .env to the new tag
docker compose pull
docker compose up -d
```

Migrations run at boot (see [Database](database.md)). Keep the `tls-certs`
volume and agents reconnect without re-installing. See
[Upgrading](upgrading.md) for management-plane and agent compatibility.

`docker compose restart kubemg` picks up a certificate you just dropped into
`ssl/`; that directory is read once at boot.

## Backup

- `postgres-data` (or a managed PostgreSQL backed up your usual way, see
  [Database](database.md)).
- `session-recordings`, with `KUBEMG_SESSION_RECORDING_KEY` **kept
  separately**; a key beside its ciphertext defends nothing.
- `KUBEMG_SECRET_KEY`, **kept separately** from the database backup. Stored
  credentials are encrypted under it and the server refuses to start on a
  restored database without it.

## Using a real certificate

```bash
cp fullchain.pem deploy/compose/ssl/tls.crt
cp privkey.pem   deploy/compose/ssl/tls.key
chmod 644 deploy/compose/ssl/tls.crt deploy/compose/ssl/tls.key
docker compose restart kubemg
```

`fullchain.pem` and `privkey.pem` are also recognized under those names, so a
certbot live directory can be mounted at `/etc/kubemg/ssl` as-is. See
[TLS and certificates](tls.md) for formats and agent trust.
