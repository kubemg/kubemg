# Upgrading

This page covers upgrading the management plane and its agents: pinning versions, when agents must re-apply their manifests, per-release notes, rollback, and the two special cases (0.11.0 and a git checkout). Start with the first sections, then jump to the release you are crossing.

## Version pinning

Pin an explicit tag rather than tracking `latest`, in three places:

| Image | Set with | Read when |
|---|---|---|
| Management plane (`ghcr.io/kubemg/kubemg:0.13.0`) | `KUBEMG_IMAGE`/`KUBEMG_VERSION` in Compose, or the Deployment's `image:` in Kubernetes | The server starts |
| Agent (`ghcr.io/kubemg/kubemg-agent:0.13.0`) | `KUBEMG_AGENT_IMAGE` | A package is rendered. Changing it affects *future* installs, not agents already running. |
| Browser shell (`ghcr.io/kubemg/kubemg-shell:0.13.0`) | `KUBEMG_SHELL_IMAGE` | A shell is *started*. Changing it affects the next shell, not one already open. |

All three are published as multi-arch (amd64 and arm64) images on a `v*` tag, after a vulnerability scan gate.

## Upgrading the management plane

```bash
# Docker Compose
docker compose pull
docker compose up -d

# Kubernetes
kubectl set image deployment/kubemg kubemg=ghcr.io/kubemg/kubemg:<new-version> -n kubemg
```

Schema migrations run automatically at boot. There is no separate migration step. See [Database](database.md) if a DBA wants to review the reference DDL first.

**Keep the certificate across the upgrade.** Every installed agent has the certificate pinned, and a fresh one is refused by all of them. A minted pair is also kept in the database ([TLS](tls.md#the-minted-certificate-is-kept-in-the-database-too)); an install upgraded from a version that kept it only on disk copies it in on the first boot, so keep the volume for that boot.

## Agent and server version compatibility

Agent and server must agree on a tunnel protocol version exactly. The server **refuses a handshake at any other version**, so a mismatched agent fails to connect entirely rather than half working.

- A protocol bump is a breaking change and is flagged in the release notes. The last one added the streaming frames behind `watch`, `logs -f`, `exec` and `port-forward`.
- An agent much older than the server may need upgrading before it can reconnect. Check the release notes when crossing more than a couple of minor versions.
- The fix is upgrading the agent: re-apply the install package rendered by the *upgraded* server (or update `KUBEMG_AGENT_IMAGE` and re-apply). It is not a config change.

## When agents must re-apply their manifests

The agent's Kubernetes manifests (ClusterRoles and bindings) can gain permissions between releases with no protocol change. Until an existing install **re-applies its manifests**, the symptom is silent and specific: one feature fails with the cluster's own `403` while the tunnel stays up.

| Release | What the manifests gained | Symptom without a re-apply |
|---|---|---|
| CRD discovery | CRD discovery and custom-resource read/write RBAC | Discovery answers `403`; Explore shows no custom resources |
| 0.8.1 | Browser shell Role, bound to the `kubemg:shell-runner` user, in the agent namespace only | Opening a shell fails with `403` at pod creation |
| 0.8.3 | `get` on `pods/exec` (exec opens as a GET over a WebSocket) | The shell pod starts, then `403` while writing its kubeconfig |
| 0.11.0 | **Narrowed** impersonation: only the four `kubemg:` groups, no ServiceAccount | Nothing breaks, but the agent keeps a wider grant than needed. Re-apply anyway. |
| Alarms on an object | `kubemg-custom-resource-view`/`-edit` gain `monitoring.coreos.com/prometheusrules` (that resource only) | Creating an [alarm](../observability/alerts.md) fails with `403`; a drawer's alarm list says it cannot read them |

Re-applying is the same command as installing. Open the cluster's dashboard and choose **Agent install** (admin-only, agent-mode clusters). It re-renders the package from the stored registration token against current settings, so it carries the new agent image too, and mints a **single-use** download URL: the first fetch of either form spends it, and an unused one expires after 15 minutes.

```bash
# Kustomize form: fetch and extract first (Kustomize accepts only local paths and Git specs)
curl -sfL https://your-kubemg/install/<download-ticket>/kustomize.tar.gz | tar -xz
kubectl apply -k kubemg-agent

# or the flat manifest, the one-liner the console shows first
kubectl apply -f https://your-kubemg/install/<download-ticket>/agent.yaml
```

If you manage the manifests yourself, diff the agent's `rbac.yaml` (`deploy/kustomize/base/`) at the new version against what is applied. The cluster detail page and the wizard's last step show whether an attached cluster's RBAC is current.

## Upgrade notes by release

A release that asks something beyond pulling the image is listed here.

- **0.11.0**, from 0.10.x: [read the section below](#upgrading-to-0110) before you pull.
- **0.11.1**, from 0.11.0: nothing beyond the pull, no re-apply. It adds the pod [Debug action](../clusters/terminals-and-logs.md#debugging-a-pod-with-no-shell), whose container runs `busybox:1.36` by default. An air-gapped install should mirror an image with a shell and point `debug_image` at it.
- **0.12.0**, from 0.11.x: no schema step, but:
    - **Re-apply every agent's install package.** An older agent silently drops `kubectl exec -i` stdin past 4 KiB and truncates `kubectl cp` into a pod. The protocol is unchanged, so old agents stay attached until you do.
    - **Run exactly one replica.** An agent's tunnel lives in the memory of the replica it reached; a second replica answers `503` for the other's clusters. Scale to one and use `strategy: Recreate`.
    - **Keep the TLS volume for the first boot** ([TLS](tls.md#the-minted-certificate-is-kept-in-the-database-too)).

    It also adds the [Helm chart](kubernetes.md) and an image pull secret for an authenticated mirror ([Air-gapped installs](air-gapped.md)); the secret reaches an agent through the same re-apply.
- **0.13.0**, from 0.12.x: no schema step (new columns are added at boot) and the agent binary is unchanged. **Re-apply every agent's install package** if anyone will create [alarms](../observability/alerts.md) (see [above](#when-agents-must-re-apply-their-manifests)). It also adds Alertmanager as a third datasource kind (*alerts*), Okta as an identity provider type, and the namespace block on Explore's workload lists. None needs anything at upgrade time.

An install that runs from a clone of the repository has one more thing to get right: see [Upgrading a git checkout](#upgrading-a-git-checkout).

## Rollback

Migrations only add tables and columns, never drop or rename, so a database migrated forward is still a valid schema for an older version. Rolling back the image is safe: pin the previous tag and run `docker compose pull && docker compose up -d`.

A rollback does **not** undo:

- **A protocol bump.** Agents already re-applied against the newer version fail to handshake with the older server until re-applied against it.
- **Data written under a newer schema's meaning.** Test an upgrade against a restored copy of production first.

## Documentation versioning

This manual is versioned against release tags. An install running `0.13.0` matches the `0.13.0` docs; check the version selector.

## Upgrading to 0.11.0

0.11.0 is a security release, from 0.10.x. Three changes alter what an existing install does, so this section goes in order: before you pull, the upgrade, what to re-apply, how to verify.

| Change | What you do |
|---|---|
| Every account reaches the cluster as `kubemg:u:<username>` | Rebind any RoleBinding you wrote against a bare kubemg username. Before the upgrade. |
| Usernames containing `:` are refused | Nothing. Existing ones keep working and are listed at boot. |
| Old install URLs answer `410 Gone` | Replace stored install URLs in runbooks or automation. Rotate the token if one leaked. |
| The agent's manifests changed | Re-apply on every agent-mode cluster. Each agent restarts once. |
| Credentials can be encrypted at rest | Optional. Back up the database first, then set `KUBEMG_SECRET_KEY`. |
| Three schema changes | Nothing. Applied at first boot. |

### 1. Before you pull

**Back up the database.** Everything here rolls back cleanly except the encryption in step 4. See [Database](database.md).

**Find bindings to bare kubemg usernames.** Accounts are now impersonated as `kubemg:u:<username>`, so `ada` becomes `kubemg:u:ada` in the API server's audit log, in `kubectl auth can-i --as`, and in the **Impersonated as** field of kubemg's trail. This closes a privilege escalation (see [Why the username is prefixed](../access/model.md#why-the-username-is-prefixed)). On each cluster, list bindings whose subject is a user:

```bash
kubectl get rolebindings,clusterrolebindings -A -o json \
  | jq -r '.items[] | select(any(.subjects[]?; .kind=="User"))
           | "\(.metadata.namespace // "-")\t\(.metadata.name)\t\([.subjects[] | select(.kind=="User") | .name] | join(","))"'
```

A subject such as `kind: User, name: ada` stops matching after the upgrade. Add `kubemg:u:ada` beside it now and remove the old name afterwards. Bindings to the `kubemg:` groups and kubemg's fixed identities (`kubemg:alarm-watcher`, `kubemg:event-watcher`, `kubemg:shell-runner`) are unaffected.

**Find stored install URLs.** Old URLs carried the cluster's registration token (`/install/kmg_.../agent.yaml`). They now carry a **single-use download ticket** ([What the install command fetches](../clusters/agent.md#what-the-install-command-fetches)) and every old URL answers `410 Gone`. Check runbooks, CI jobs, GitOps bootstrap scripts and wikis, and use a fresh URL from **Agent install**.

**Update audit and SIEM rules** that match `impersonate_user` against a bare username. Old records keep the bare name; new ones carry the prefix.

### 2. Upgrade the management plane

Pin `0.11.0` and pull as in [Upgrading the management plane](#upgrading-the-management-plane):

```bash
# Docker Compose (KUBEMG_IMAGE=ghcr.io/kubemg/kubemg:0.11.0 in .env)
docker compose pull
docker compose up -d
```

First boot applies three schema changes: it widens the recorded impersonated identity to 190 characters, adds a table for install tickets, and adds a lookup column for agent tokens. Attached agents stay attached; the upgrade does not change their tokens.

### 3. Re-apply the agent manifests

On every agent-mode cluster, open the dashboard, choose **Agent install** and run the command ([above](#when-agents-must-re-apply-their-manifests)). The re-apply:

- **Narrows the impersonation grant** to kubemg's four `kubemg:` groups and no ServiceAccount. Skipping it breaks nothing but leaves the wider grant.
- **Restarts the agent pod once.** The pod template now carries a fingerprint of the agent Secret so a package applied after a token rotation restarts the agent. The tunnel is down for those seconds.
- **Records `agent-displaced` once per cluster** in the trail. Expected here; see [When a connection displaces the agent](../clusters/agent.md#when-a-connection-displaces-the-agent).

**If an old install URL may have been copied somewhere you do not control**, its token is still valid. On the cluster's dashboard choose **Rotate agent token**, then apply the package the console shows. The agent is down between the two steps ([Rotating the registration token](../clusters/agent.md#rotating-the-registration-token)).

### 4. Optional: encrypt credentials at rest

kubemg can encrypt the credentials it stores: its signing key, agent registration tokens, direct-mode ServiceAccount tokens, datasource, Helm repository and alarm credentials, the OIDC client secret and the LDAP bind password. Nothing changes until you set `KUBEMG_SECRET_KEY`; until then the server warns at every boot and the posture page flags it.

1. Generate a key with `openssl rand -base64 32`. Store it **separately** from the database backup.
2. Set `KUBEMG_SECRET_KEY` and restart. The first boot encrypts everything in place.
3. Nothing else changes: agents reconnect with the same tokens, and sessions and kubeconfigs stay valid.

**From here on the key is as important as the database.** A server with a different key, or none, over an encrypted database refuses to boot. **Rolling back to 0.10.x after setting the key does not work**; the way back is the step 1 backup ([Database](database.md#credentials-encrypted-at-rest)).

### 5. Check that it worked

- The console footer reads `kubemg 0.11.0`.
- Every agent-mode cluster shows its agent attached after the re-apply.
- After one call through the console or a kubeconfig, the audit trail's **Impersonated as** reads `kubemg:u:<your username>`.
- The server log has no `accounts carry a username new accounts may no longer take` line. If it lists accounts, they contain `:`; they keep working and can be renamed in the user editor. A federated user with such a name who has never signed in is refused at first sign-in.
- If you enabled encryption: the posture page no longer flags the secret key.
- An old `/install/kmg_...` URL returns `410`.

### Rolling back from 0.11.0

Without `KUBEMG_SECRET_KEY` set, pin `0.10.0` again and pull; the schema changes only add. After a rollback the cluster sees bare usernames again, so keep both subjects in any rebound RoleBinding until you are sure. An agent re-applied from 0.11.0 keeps its narrowed grant and keeps working. With the key set, restore the database from the step 1 backup.

## Upgrading a git checkout

This section is for an install that runs from a clone of the repository (`git clone`, then `make up` or `docker compose up` at the repository root), which is the **dev stack** the [Quickstart](../getting-started/quickstart.md) uses. It is fine for evaluating, not for production. To move to a production install, see [Moving to the image-based install](#moving-to-the-image-based-install).

### The dev stack's signing key is public

The root `docker-compose.yml` sets `JWT_SECRET=kubemg_dev_secret_change_me`, readable in the repository. That key signs every session and agent-mode kubeconfig, so anyone who knows it can mint a super-admin session and reach every agent-mode cluster. **If a dev-stack install is reachable by anyone you would not hand cluster-admin to, treat its sessions as compromised.** Set your own value now:

```bash
# in .env at the repository root (gitignored, survives every pull)
JWT_SECRET=$(openssl rand -base64 48)
```

Restart with `make up`. This signs everyone out and invalidates every issued **agent-mode kubeconfig**, so people download new ones. Machine-account tokens are stored, not signed, and keep working.

### "You have divergent branches and need to specify how to reconcile them"

`git pull` stops with this when the checkout's `master` has commits `origin/master` lacks. Either somebody committed on the install host (usually a port or env var edited into `docker-compose.yml`), or upstream history was rewritten and git counts every commit as yours.

Find commits whose content `origin/master` lacks (this compares changes, not ids):

```bash
git fetch origin
git cherry -v origin/master HEAD | grep '^+'
```

**No output**: nothing on the host is worth keeping. Keep a pointer and move to upstream:

```bash
git branch backup-before-upgrade
git reset --hard origin/master
```

**Some output**: these are real local changes. Configuration belongs in `.env` at the repository root, not in a commit; move it there, then reset as above. A genuine code change can be carried with `git pull --rebase`.

Do **not** answer the prompt with `git config pull.rebase true` before you know which case you are in. Rebasing rewritten history replays every upstream commit and ends in meaningless conflicts.

### What a reset keeps

`git reset --hard` only rewrites tracked files. Untouched: `.env` (gitignored) and the Docker volumes `postgres-data` (users, grants, clusters, audit trail), `tls-certs` (the certificate every agent pinned) and `session-recordings`.

Bring the stack back with `make up` (or `docker compose up -d --build`) and follow the [release notes](#upgrade-notes-by-release).

### Moving to the image-based install

The production [Docker Compose](docker-compose.md) install builds nothing and has no git history to diverge. Carry these across unchanged:

| Carry over | Why |
|---|---|
| The database | Everything kubemg knows. |
| The certificate in `tls-certs` | Every installed agent pinned it. A new one is refused by all of them. |
| The public address (`KUBEMG_PUBLIC_URL` and the hosts the certificate names) | Agents dial it and issued kubeconfigs point at it. |
| `KUBEMG_SECRET_KEY` and `KUBEMG_SESSION_RECORDING_KEY`, if set | The database and recordings are encrypted under them. |

Do **not** carry the dev `JWT_SECRET` across; set a new one. With the dev stack at the repository root and the new install in `deploy/compose/` on the same host:

```bash
# 1. Stop kubemg but keep the database up, and dump it.
docker compose stop backend frontend
docker compose exec -T postgres pg_dump -U kubemg -Fc kubemg > kubemg.dump

# 2. Copy the certificate out of the dev stack's volume. The volume name is
#    prefixed with the clone's directory, e.g. kubemg_tls-certs.
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

# 5. Start kubemg. The dev stack stays stopped; both would bind :8443.
docker compose up -d
```

Use the database user and name from your `.env` if you changed them. Copy `session-recordings` the same way if you need the replays. Then check:

- The server log does **not** say it generated a certificate. If it does, step 4's copy did not land and every agent will be refused. Stop and fix the volume; nothing is lost while the old one exists.
- Every agent-mode cluster shows its agent attached, with no re-apply.
- You can sign in with an existing account.

Keep the dev stack's volumes until the new install has run a while. They are the way back.

## Next

- [Database](database.md)
- [Production checklist](production-checklist.md)
- [The agent](../clusters/agent.md)
