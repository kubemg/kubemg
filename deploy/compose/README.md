# KubeMG on a single machine

A deployment compose file: it **pulls published images and builds nothing**, so
it runs on a host with no toolchain, no source checkout and no route to the
internet beyond a registry you control.

This is not the dev stack. `docker-compose.yml` at the repository root builds
from source and bind-mounts it, and `make up` is still the way to run that. The
two are unrelated and can coexist.

## Install

```bash
cp .env.example .env
$EDITOR .env          # four values are required; see below
docker compose up -d
```

Then open `https://<your-host>:8443` and sign in with the administrator from
`.env`. The certificate is self-signed on first boot, so a browser will warn
once.

Four values have no default, and compose refuses to start without them rather
than bringing up a bastion with a password from an example file:

| Variable | What it is |
| --- | --- |
| `DB_PASSWORD` | Postgres password. Nothing outside the compose network reaches it — it publishes no port — but generate one anyway. |
| `JWT_SECRET` | Signs every session token and generated kubeconfig. Changing it revokes all of them at once. |
| `KUBEMG_ADMIN_PASSWORD` | The first administrator, created on first boot only. Change it later in the console, not here. |
| `KUBEMG_PUBLIC_URL` | The address **target clusters** dial to reach this host. |

`KUBEMG_PUBLIC_URL` is the one that is easy to get wrong and hard to diagnose:
it is baked into every rendered agent manifest, so `localhost` produces an agent
that dials itself and never connects. Use the LAN, VPN or DNS name, with the
port. Every other name the certificate must be valid for goes in
`KUBEMG_TLS_HOSTS`, or the cluster's handshake fails.

## What it runs

Two containers — `postgres` and `kubemg` — and one image your *clusters* pull,
never this host:

| Image | Pulled by | Why |
| --- | --- | --- |
| `kubemg` | this host | The management plane: console and gateway in one binary. |
| `postgres:16-alpine` | this host | Users, grants, clusters, the audit trail. |
| `kubemg-agent` | **your target clusters** | The outbound tunnel. Named in `KUBEMG_AGENT_IMAGE`. |

The console is served by the `kubemg` container from the same origin as the API
it calls, which is why there is no CORS setting here. The dev stack needs one
because Vite serves the console on a separate port.

## Air-gapped installs

Mirror the three images above into an internal registry and point the install at
them:

```dotenv
KUBEMG_IMAGE=registry.internal/kubemg/kubemg:0.3.0
KUBEMG_POSTGRES_IMAGE=registry.internal/postgres:16-alpine
KUBEMG_AGENT_IMAGE=registry.internal/kubemg/kubemg-agent:0.3.0
```

Nothing else is fetched at runtime: the console's fonts are served out of the
binary rather than a CDN, and no page calls an external host.

`KUBEMG_AGENT_IMAGE` is the one that has to be reachable **from your clusters**,
not from this host — it is written into the manifests operators apply. A mirror
that requires authentication is not yet supported for the agent: the rendered
manifests carry no `imagePullSecrets`.

## The two volumes, and which one to back up

| Volume | Holds | If you lose it |
| --- | --- | --- |
| `tls-certs` | The certificate minted on first boot | **Every installed agent stops connecting.** It pinned this certificate; a fresh one is a different certificate and the handshake fails. Back this up. |
| `session-recordings` | Encrypted `.cast.gz` session replays | Audit evidence is gone. Recordings are the artefact an auditor asks for. |
| `postgres-data` | Users, grants, clusters, audit trail | The install is gone. |

Set `KUBEMG_SESSION_RECORDING_KEY` before anyone opens a shell, or recordings
are written in plaintext — the server warns about this at boot. Keep the key
somewhere other than this host; a key stored beside the ciphertext protects
nobody.

## Using a real certificate

Mount the certificate and key over `/etc/kubemg/tls` and turn the self-signed
path off:

```yaml
    volumes:
      - /etc/ssl/kubemg:/etc/kubemg/tls:ro
    environment:
      - KUBEMG_TLS_SELF_SIGNED=false
```

The files must be named `tls.crt` and `tls.key`, or set `KUBEMG_TLS_CERT_FILE`
and `KUBEMG_TLS_KEY_FILE`. With a certificate agents' trust stores already
recognise, the pinning stops mattering — which is what makes replacing the
bastion host straightforward rather than a fleet-wide reinstall.

TLS itself is not optional: client-go refuses to send a bearer token over plain
HTTP, so a plaintext bastion cannot serve a generated kubeconfig or an `exec`
session at all.

## Upgrading

```bash
# edit KUBEMG_IMAGE in .env to the new tag
docker compose pull
docker compose up -d
```

Schema migrations run at boot. Keep the `tls-certs` volume across the upgrade
and the fleet's agents reconnect on their own.
