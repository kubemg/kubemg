# KubeMG on a single machine

A deployment compose file: it **pulls published images and builds nothing**, so
it runs on a host with no toolchain, no source checkout and no route to the
internet beyond a registry you control.

This is not the dev stack. `docker-compose.yml` at the repository root builds
from source and bind-mounts it, and `make up` is still the way to run that. The
two are unrelated and can coexist.

## Install

```bash
docker compose up -d
docker compose logs kubemg | grep -A6 'not configured yet'
```

That is the whole install. The second line reads the administrator password,
which is generated on first boot and printed exactly once. Then open
`https://<your-host>:8443`, sign in, and the console walks you through setup —
the address clusters dial, the agent image, what the trail keeps, optionally an
SSO provider — before it lets you register anything. The certificate is
self-signed on first boot, so a browser will warn once.

Setup will not finish until that generated password is changed. Everything it
collects is stored in the database and editable afterwards from **Settings**.

## Deciding it up front instead

Copy `.env.example` to `.env` and set what you want to decide yourself; anything
in it wins over what setup would have asked for. This is what you want when the
install is scripted, when secrets come out of a manager, or when several
replicas have to agree on a signing key.

| Variable | What it is |
| --- | --- |
| `DB_PASSWORD` | Postgres password. Nothing outside the compose network reaches it — it publishes no port — but generate one rather than leaving the default. |
| `JWT_SECRET` | Signs every session token and generated kubeconfig. Unset, the server mints one on first boot and keeps it in the database, so sessions survive a restart. Changing it revokes all of them at once. |
| `KUBEMG_ADMIN_PASSWORD` | The first administrator, created on first boot only. Unset, one is generated and logged. Change it later in the console, not here. |
| `KUBEMG_PUBLIC_URL` | The address **target clusters** dial to reach this host. |

`KUBEMG_PUBLIC_URL` is the one that is easy to get wrong and hard to diagnose:
it is baked into every rendered agent manifest, so `localhost` produces an agent
that dials itself and never connects. Use the LAN, VPN or DNS name, with the
port. It is also the one field setup will not let you past without, so leaving
it here is only a question of whether you would rather answer it now or in the
browser. Every other name the certificate must be valid for goes in
`KUBEMG_TLS_HOSTS` — that one is read at boot and setup cannot change it, so a
host you will need later belongs here now.

## What it runs

Two containers — `postgres` and `kubemg` — and one image your *clusters* pull,
never this host:

| Image | Pulled by | Why |
| --- | --- | --- |
| `kubemg` | this host | The management plane: console and gateway in one binary. |
| `postgres:16-alpine` | this host | Users, grants, clusters, the audit trail. |
| `kubemg-agent` | **your target clusters** | The outbound tunnel. Named in `KUBEMG_AGENT_IMAGE`. |

Both KubeMG images are published to GitHub's registry — `ghcr.io/kubemg/kubemg`
and `ghcr.io/kubemg/kubemg-agent`, each a manifest index covering amd64 and
arm64 — and are public, so no `docker login` is needed to pull them.

The console is served by the `kubemg` container from the same origin as the API
it calls, which is why there is no CORS setting here. The dev stack needs one
because Vite serves the console on a separate port.

## Air-gapped installs

Mirror the three images above into an internal registry and point the install at
them:

```dotenv
KUBEMG_IMAGE=registry.internal/kubemg/kubemg:0.6.0
KUBEMG_POSTGRES_IMAGE=registry.internal/postgres:16-alpine
KUBEMG_AGENT_IMAGE=registry.internal/kubemg/kubemg-agent:0.6.0
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

Copy it into the `ssl` directory next to `docker-compose.yml` and restart. There
is nothing to configure — that directory is already mounted at `/etc/kubemg/ssl`,
and it is the first place the server looks:

```bash
cp fullchain.pem ssl/tls.crt
cp privkey.pem   ssl/tls.key
chmod 644 ssl/tls.crt ssl/tls.key
docker compose restart kubemg
```

`fullchain.pem` + `privkey.pem` are recognised under those names as well, so a
certbot live directory can be mounted at `/etc/kubemg/ssl` instead of copied out
of. Nothing else in the directory is read: names are fixed rather than searched
for, because a directory scanned for "something that looks like a certificate" is
one where renaming a file quietly changes what the bastion serves.

The server runs unprivileged (uid `65532`), so the files have to be readable by
it — a key left at certbot's root-only `0600` stops the boot with a message
saying so, as does half a pair or a certificate and key that do not match. None
of those fall back to the self-signed certificate: an operator who mounted a
certificate believes it is the one in force, and a silent fallback would pin that
fallback into every agent package they hand out next.

A certificate here wins over the pair in the `tls-certs` volume, which is what
makes this work on an install whose first boot already minted one. Setting
`KUBEMG_TLS_SELF_SIGNED=false` is the stricter version of the same decision: it
refuses to start without a real certificate rather than minting one. And
`KUBEMG_TLS_CERT_FILE` / `KUBEMG_TLS_KEY_FILE` still name explicit paths for an
install that was already configured that way.

With a certificate agents' trust stores already recognise, the pinning stops
mattering — which is what makes replacing the bastion host straightforward rather
than a fleet-wide reinstall. **Settings → Deployment** reports which of the two
the running server is serving, along with everything else settled at boot:
whether recordings are encrypted, and where the signing key came from.

TLS itself is not optional: client-go refuses to send a bearer token over plain
HTTP, so a plaintext bastion cannot serve a generated kubeconfig or an `exec`
session at all. And a plaintext listener reachable from more than the host is now
refused at boot rather than warned about — this service publishes a port, so
`KUBEMG_TLS_ENABLED=false` alone stops the container instead of starting one that
sends every session token in the clear. Behind a proxy that terminates TLS, add
`KUBEMG_ALLOW_INSECURE=true` to say so deliberately, and set
`KUBEMG_AGENT_CA_BUNDLE` to the chain **that proxy** presents: agents verify its
certificate, not one this process can see, so nothing here can infer it.

## Upgrading

```bash
# edit KUBEMG_IMAGE in .env to the new tag
docker compose pull
docker compose up -d
```

Schema migrations run at boot. Keep the `tls-certs` volume across the upgrade
and the fleet's agents reconnect on their own.
