# Upgrading a git checkout

This page is for an install that runs from a clone of the repository — `git clone`,
then `make up` or `docker compose up` at the repository root — rather than from the
published images. That is the **dev stack**. It is what the
[Quickstart](../getting-started/quickstart.md) uses, and it is fine for evaluating kubemg.
It is **not** a production install, and an install that has grown into production
from it should move to [Docker Compose](docker-compose.md) or
[Kubernetes](kubernetes.md). The last section below is how to do that without
losing anything.

## Before anything else: the dev stack's signing key is public

The root `docker-compose.yml` sets `JWT_SECRET=kubemg_dev_secret_change_me`, a
value anyone can read in the repository. That key signs every session and every
agent-mode kubeconfig, so anyone who knows it can mint a super-admin session and,
through the tunnel, reach every agent-mode cluster. **If a dev-stack install
is reachable by anyone you would not hand cluster-admin to, treat its sessions
as compromised.** Set your own value today, whether or not you upgrade:

```bash
# in .env at the repository root (gitignored, survives every pull)
JWT_SECRET=$(openssl rand -base64 48)
```

The dev compose file reads it from there, falling back to the public value only
when `.env` does not set one. Or move to the image-based install below, which
has no default. Restart with `make up` for it to take effect. Changing the key signs everyone
out and invalidates every issued **agent-mode kubeconfig**, so people download
new ones. That is what you want after a key was public. Machine-account tokens
are stored, not signed, and keep working.

## "You have divergent branches and need to specify how to reconcile them"

`git pull` stops with this when the checkout's `master` has commits that
`origin/master` does not. There are two ways that happens, and which one it is
decides what to do.

- **Somebody committed on the install host.** It is usually a port, an image or
  an environment variable edited into `docker-compose.yml` and committed.
- **The upstream history was rewritten.** If `master` was ever force-pushed, a
  checkout made before that holds the same changes under different commit ids,
  and git counts every one of them as yours.

Ask git which commits carry content `origin/master` does not have. It compares
changes, not commit ids, so rewritten history does not show up:

```bash
git fetch origin
git cherry -v origin/master HEAD | grep '^+'
```

**No output** means nothing on the host is worth keeping. Keep a pointer to the
old state and move to upstream:

```bash
git branch backup-before-upgrade
git reset --hard origin/master
```

**Some output** is the list of real local changes. Look at each one. A
configuration value belongs in `.env` at the repository root, which is
gitignored and read by `docker-compose.yml`, not in a commit. Move it there and
then reset as above. A genuine code change you need to keep can be carried with
`git pull --rebase` instead.

Do **not** answer the prompt with `git config pull.rebase true` before you know
which case you are in. Rebasing a rewritten history replays every upstream
commit a second time and ends in conflicts that mean nothing.

## What a reset keeps

`git reset --hard` only rewrites files git tracks. These are untouched:

- `.env` at the repository root. It is gitignored.
- The Docker volumes: `postgres-data` (users, grants, clusters, the audit trail),
  `tls-certs` (the certificate every agent pinned) and `session-recordings`.
  git does not know they exist.

So after the reset, bring the stack back with `make up` (or
`docker compose up -d --build`) and follow the
[release's upgrade notes](upgrading.md#upgrade-notes-by-release) before you
consider it done.

## Moving to the image-based install

The production [Docker Compose](docker-compose.md) install builds nothing and
has no git history to diverge. Upgrading it is a version number and
`docker compose pull`. Four things must come across unchanged or
something that worked stops working:

| Carry over | Why |
|---|---|
| The database | Everything kubemg knows. |
| The certificate in `tls-certs` | Every installed agent pinned it. A new one is refused by all of them at once. |
| The public address (`KUBEMG_PUBLIC_URL`, and the hosts the certificate names) | It is what agents dial and what issued kubeconfigs point at. |
| `KUBEMG_SECRET_KEY` and `KUBEMG_SESSION_RECORDING_KEY`, if set | The database and the recordings are encrypted under them. |

`JWT_SECRET` is the exception: do **not** carry the dev value across. Set a new
one (see the first section).

A migration, with the dev stack at the repository root and the new install in
`deploy/compose/` on the same host:

```bash
# 1. Stop kubemg but keep the database up, and dump it.
docker compose stop backend frontend
docker compose exec -T postgres pg_dump -U kubemg -Fc kubemg > kubemg.dump

# 2. Copy the certificate out of the dev stack's volume. The volume name is
#    prefixed with the directory the clone lives in, e.g. kubemg_tls-certs.
docker volume ls | grep tls-certs
docker run --rm -v kubemg_tls-certs:/from -v "$PWD":/to alpine \
  sh -c 'mkdir -p /to/tls-backup && cp -a /from/. /to/tls-backup/'

# 3. Prepare deploy/compose/.env from .env.example: the same public URL and
#    TLS hosts, the same KUBEMG_SECRET_KEY / KUBEMG_SESSION_RECORDING_KEY,
#    a new JWT_SECRET, and KUBEMG_IMAGE pinned to the release.

# 4. Start only its database, restore into it, then seed the certificate
#    volume before kubemg first boots. Otherwise it mints a new certificate.
cd deploy/compose
docker compose up -d postgres
docker compose exec -T postgres pg_restore -U kubemg -d kubemg --clean --if-exists < ../../kubemg.dump
docker volume ls | grep tls-certs      # e.g. compose_tls-certs
docker run --rm -v compose_tls-certs:/to -v "$OLDPWD":/from alpine \
  sh -c 'cp -a /from/tls-backup/. /to/'

# 5. Start kubemg. The dev stack stays stopped. Both would bind :8443.
docker compose up -d
```

Use the database user and name from your own `.env` if you changed them. Copy
`session-recordings` the same way as the certificate if you need the replays.

Then check that it worked:

- The server log does **not** say it generated a certificate. If it does,
  step 4's copy did not land, and every agent will be refused. Stop, fix the
  volume and start again. Nothing is lost while the old volume still exists.
- Every agent-mode cluster shows its agent attached, without re-applying
  anything.
- You can sign in with an existing account.

Keep the dev stack's volumes until the new install has run for a while. They are
the way back.
