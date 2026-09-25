# Upgrading to 0.11.0

0.11.0 is a security release. Three of its changes alter what an existing
install does, so this page goes in the order you meet them: what to check
before you pull, the upgrade itself, what to re-apply after it, and how to
tell that it worked. The general procedure (image pinning, rollback, the
certificate volume) is on [Upgrading](upgrading.md). If kubemg runs from a
clone of the repository, read [Upgrading a git checkout](upgrading-from-source.md)
first.

## At a glance

| Change | What you do |
|---|---|
| Every account reaches the cluster as `kubemg:u:<username>` | Rebind any RoleBinding you wrote against a bare kubemg username. Before the upgrade. |
| Usernames containing `:` are refused | Nothing, now. Existing ones keep working and are listed at boot. |
| Old install URLs answer `410 Gone` | Replace any stored install URL in runbooks or automation. Rotate the token if one leaked. |
| The agent's manifests changed | Re-apply them on every agent-mode cluster. Each agent restarts once. |
| Credentials can be encrypted at rest | Optional. Take a database backup first, then set `KUBEMG_SECRET_KEY`. |
| Three schema changes | Nothing. Applied at first boot. |

## 1. Before you pull

**Take a database backup.** Everything in this release rolls back cleanly
except the encryption in step 4. Once the key is set, going back to 0.10.x
means restoring this backup. See [Database](database.md).

**Find bindings to bare kubemg usernames.** From this release every kubemg
account is impersonated as `kubemg:u:<username>` rather than as the bare
username, so `ada` becomes `kubemg:u:ada` in the API server's audit log, in
`kubectl auth can-i --as`, and in the **Impersonated as** field of kubemg's
own trail. It closes a privilege escalation: without the prefix, an account
named like a ServiceAccount or a `system:` identity was that identity to the
cluster (see
[Why the username is prefixed](../access/model.md#why-the-username-is-prefixed)).

On each cluster, list the bindings whose subject is a user:

```bash
kubectl get rolebindings,clusterrolebindings -A -o json \
  | jq -r '.items[] | select(any(.subjects[]?; .kind=="User"))
           | "\(.metadata.namespace // "-")\t\(.metadata.name)\t\([.subjects[] | select(.kind=="User") | .name] | join(","))"'
```

A subject that is a kubemg username, such as `kind: User, name: ada`, stops
matching after the upgrade. Add `kubemg:u:ada` beside it now, and remove the old
name after the upgrade. Bindings to the `kubemg:` groups are unaffected, and
they are how kubemg's own manifests grant everything. kubemg's fixed
identities are unaffected too: `kubemg:alarm-watcher`, `kubemg:event-watcher`
and `kubemg:shell-runner`.

**Find stored install URLs.** Install URLs used to carry the cluster's
registration token in the path (`/install/kmg_…/agent.yaml`), and that token is
the agent's permanent credential. From this release the URL carries a
**single-use download ticket** instead (see
[What the install command fetches](../clusters/agent.md#what-the-install-command-fetches)),
and every old URL answers `410 Gone`. Look in runbooks, CI jobs, GitOps
bootstrap scripts and wiki pages. Replace each with a fresh URL taken from
**Agent install** at the time of use, or with manifests you manage yourself.

**Update audit and SIEM rules** that match `impersonate_user` against a bare
username. Records written before the upgrade keep the bare name. Records after
it carry the prefix.

## 2. Upgrade the management plane

Pin `0.11.0` and pull, as on [Upgrading](upgrading.md#upgrading-the-management-plane):

```bash
# Docker Compose (KUBEMG_IMAGE=ghcr.io/kubemg/kubemg:0.11.0 in .env)
docker compose pull
docker compose up -d
```

The first boot applies three schema changes on its own. It widens the
recorded impersonated identity to 190 characters for the prefix, adds a table
for install tickets, and adds a lookup column for agent tokens.

Attached agents stay attached. They authenticate with the token in their own
Secret, not with a URL, and the upgrade does not change it.

## 3. Re-apply the agent manifests

Do this on every agent-mode cluster. Open the cluster's dashboard, choose
**Agent install**, and run the command it shows. It is the same command as a
first install (see
[When agents must re-apply their manifests](upgrading.md#when-agents-must-re-apply-their-manifests)).

What the re-apply changes:

- **The agent's impersonation grant narrows.** It may now impersonate only
  kubemg's four `kubemg:` groups and no ServiceAccount. Nothing breaks if you
  skip this, because kubemg sends the same groups either way. But the old
  grant is the wider one, and an agent that is not re-applied keeps a
  privilege kubemg no longer needs.
- **The agent pod restarts once.** The pod template now carries a fingerprint
  of the agent Secret (`kubemg.io/secret-checksum`), so that a package applied
  after a token rotation actually restarts the agent. The first re-apply
  changes the template, so every agent restarts once. Its tunnel is down for
  those seconds.
- **The trail records `agent-displaced` once per cluster.** kubemg now records
  every connection that takes over a cluster's tunnel, and the restarting pod
  is one. The record is expected here. See
  [When a connection displaces the agent](../clusters/agent.md#when-a-connection-displaces-the-agent)
  for what it means outside an upgrade.

**If an old install URL may have been copied somewhere you do not control**,
the token inside it is still valid after the upgrade. Rotate it: on the
cluster's dashboard choose **Rotate agent token**, then apply the package the
console shows. The agent is down between the two steps. See
[Rotating the registration token](../clusters/agent.md#rotating-the-registration-token).

## 4. Optional: encrypt credentials at rest

This release can encrypt the credentials kubemg stores: its signing key, the
agent registration tokens, direct-mode ServiceAccount tokens, datasource,
Helm repository and alarm credentials, the OIDC client secret and the LDAP
bind password. Nothing changes until you set `KUBEMG_SECRET_KEY`. Until then
the server logs a warning at every boot, and the posture page flags it.

1. Generate a key with `openssl rand -base64 32`. Store it somewhere that is
   backed up **separately** from the database. A key kept beside the backup it
   protects defends against nothing.
2. Set `KUBEMG_SECRET_KEY` and restart. The first boot encrypts every stored
   credential in place. Later restarts find nothing left to do.
3. Nothing else changes. Agents reconnect with their unchanged tokens, and
   sessions and kubeconfigs stay valid.

**From here on the key is as important as the database.** A server started with
a different key, or with none, over an encrypted database refuses to boot
rather than guess. **Rolling back to 0.10.x after the key was set does not
work**, because the older server would read the ciphertext as the credentials
themselves. The way back is the backup from step 1. See
[Database](database.md#credentials-encrypted-at-rest).

## 5. Check that it worked

- **The version.** The console's footer reads `kubemg 0.11.0`, and so does the
  `version` field of the server's startup log line.
- **Agents.** Every agent-mode cluster shows its agent attached after the
  re-apply.
- **The identity.** Make one call through the console or a kubeconfig, then
  open the audit trail. **Impersonated as** reads `kubemg:u:<your username>`.
- **Usernames.** Search the server log for
  `accounts carry a username new accounts may no longer take`. Any account it
  lists contains `:`. It keeps working, and you can rename it in the user
  editor when convenient. The rule applies only when an account is created or
  renamed, so a federated user with such a name who has never signed in is
  refused at first sign-in.
- **Encryption, if you turned it on.** The posture page no longer flags the
  secret key, and the boot log does not warn about plaintext credentials.
- **Old URLs.** Fetching an old `/install/kmg_…` URL returns `410`.

## Rolling back

Without `KUBEMG_SECRET_KEY` set, pin `0.10.0` again and pull. The schema changes
only add, so 0.10.0 runs against the migrated database. Two things to know:

- After a rollback the cluster sees bare usernames again. Keep both subjects in
  any rebound RoleBinding until you are sure you are staying on 0.11.0.
- An agent re-applied from 0.11.0 keeps its narrowed grant. 0.10.0 sends only
  the same `kubemg:` groups, so it keeps working.

With the key set, restore the database from the backup taken in step 1.
