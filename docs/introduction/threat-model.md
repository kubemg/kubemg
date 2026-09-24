# Threat model

What an attacker reaches when each part of kubemg falls into the wrong hands,
and what bounds it. The [security model](security-model.md) explains the
controls one at a time; this page starts from the other end — the incident —
and says which of those controls is the one standing between the attacker and
the cluster, and where nothing does.

## What the bastion is

In agent mode kubemg keeps no Kubernetes credential in its database, and it is
tempting to stop there. That would be the wrong summary. The agent in every
cluster may **impersonate** — it asserts a user name and a set of groups, and
the cluster's API server believes it. It forwards whatever the bastion sends
down the tunnel without an opinion of its own. The agent's grant is narrowed as
far as Kubernetes allows: it may claim only kubemg's own four groups, never
`system:masters` or a ServiceAccount. But one of those four groups is
`kubemg:cluster-admin`, which the install binds to the built-in `cluster-admin`
role, and the user name it asserts can be any name at all, because Kubernetes
has no way to limit impersonated user names by prefix.

So, stated plainly: **the bastion plus the tunnel is, in effect,
`system:masters` on every agent-mode cluster.** Whoever controls the bastion
process controls every agent-mode cluster attached to it.

That is not unusual, and it is not an accident. It is the trust model of every
central Kubernetes access product that reaches clusters through an in-cluster
agent: Rancher's `cattle-cluster-agent` runs under a ServiceAccount bound to
full control of the cluster and carries the Rancher server's requests to it;
Teleport's Kubernetes Service impersonates users and groups on the proxy's
behalf. A central point that can grant anyone access to a cluster can, by
construction, grant itself that access. What differs between products is how
much runs inside the cluster, how narrow the in-cluster grant is, and how much
of the central point's work is written down somewhere it cannot erase. For
kubemg the answers are: one small Deployment with no controllers and no CRDs,
an impersonation grant limited to four named groups, and an audit trail that
can be [forwarded off the host](../audit/forwarding.md) as it is written.

The rest of this page follows from that. The bastion, its database and its
signing key are what you harden hardest; everything a user carries is bounded
by the bastion re-checking it on every call.

## At a glance

| Scenario | What it reaches | What bounds it |
| --- | --- | --- |
| [A compromised bastion](#a-compromised-bastion) | Every agent-mode cluster, as cluster-admin; every direct-mode cluster, as its stored ServiceAccount | Nothing inside the cluster. Off-host audit forwarding, the cluster's own audit log, and removing the agent |
| [A read of the database](#a-read-of-the-database) | With no `KUBEMG_SECRET_KEY`: the signing key, and through it the whole fleet. With one: grants, users and audit history, but no usable credential | `KUBEMG_SECRET_KEY`, and `JWT_SECRET` from the environment |
| [A leaked install URL](#a-leaked-install-url) | Nothing, if the real install used it first. Otherwise the agent package, and the ability to stand in for that cluster's agent | Single-use, 15-minute download ticket; takeover audit; token rotation |
| [A leaked kubeconfig](#a-leaked-kubeconfig) | Agent mode: one cluster, as its holder, until revoked or expired. Direct mode: one cluster, until the token expires | Revocation, grant re-check on every call, the kubeconfig lifetime ceiling |
| [A leaked machine token](#a-leaked-machine-token) | The clusters and namespaces its account is granted, agent mode only | Hashed storage, revocation on next use, no direct-mode reach |
| [A malicious or renamed IdP identity](#a-malicious-or-renamed-idp-identity) | What that provider's group mappings grant — never a local account, never another provider's, never a cluster identity outside kubemg's own | Username rule, `kubemg:u:` prefix, account matching by the provider's own id |
| [A compromised agent](#a-compromised-agent) | The same cluster-admin reach as the bastion, on that one cluster | The agent's narrowed grant; the cluster's audit log |

## A compromised bastion

**Reaches.** Everything above: every agent-mode cluster as `cluster-admin` or
as any user name those clusters have bound a role to, every direct-mode
cluster as whatever its stored ServiceAccount is bound to, every live `exec`
session as it happens, and every session recording, since the server holds
the key that decrypts them. It can also mint sessions and kubeconfigs for any
kubemg account, and it can write to its own audit trail.

**Bounds.** Nothing inside the clusters — the cluster's RBAC decides on the
identity the bastion asserts, and the bastion chooses that identity. What
limits the damage is outside the bastion:

- **Records already forwarded stay forwarded.** A
  [forwarder](../audit/forwarding.md) pushes each audit record off the host
  as it is written, so the history up to the compromise survives at the
  receiving end whatever happens to the database afterwards.
- **The cluster keeps its own record.** Every call through the agent reaches
  the API server authenticated as the agent's ServiceAccount, with the
  impersonated user named beside it. A cluster with API server auditing
  enabled records both, independently of kubemg.
- **The tunnel can be cut from the cluster side.** Deleting the agent, or the
  binding that grants it `impersonate`, ends kubemg's reach into that cluster
  at once. Nothing else kubemg installs acts on the cluster by itself.

This is why the [production checklist](../install/production-checklist.md)
treats the bastion's host, its database and its TLS material as the thing to
protect first.

## A read of the database

**Reaches.** Every user, group, grant, cluster registration, setting and audit
record, and the password hashes of local accounts. What else depends on
`KUBEMG_SECRET_KEY`:

- **Without it**, the stored credentials are readable as written: the
  generated session signing key, every agent's tunnel credential, direct-mode
  ServiceAccount tokens, and the datasource, Helm repository, alarm channel
  and SSO secrets. The signing key alone is enough to mint a super-admin
  session — which, through the tunnel, is cluster-admin on every agent-mode
  cluster. **A database dump without the key is the bastion.**
- **With it**, every one of those is AES-256-GCM ciphertext under a key that
  lives in the server's environment, not in the database. A stolen dump,
  replica or backup is no longer a working credential on its own. Machine
  tokens and install tickets were never stored in the clear at all — only
  their hashes are.

Setting `JWT_SECRET` in the environment keeps the signing key out of the
database entirely. Session recordings are files on the server's disk, not
database rows, and are encrypted under their own key when
`KUBEMG_SESSION_RECORDING_KEY` is set. See
[Credentials encrypted at rest](../install/database.md#credentials-encrypted-at-rest).

A **write** to the database is a different incident: grants and system roles
are rows, and kubemg trusts its own database. Whoever can write to it can
grant themselves anything, exactly as whoever controls the bastion can.

## A leaked install URL

**Reaches.** Very little, in the common case. The URL carries a single-use
download ticket, not the agent's credential. The first download spends it,
and an unused one expires after 15 minutes. A URL found in shell history, a
CI log or a proxy's access log after the install ran is dead.

If someone fetches it **before** the real install does, they receive the
agent package, and the package carries the cluster's tunnel credential. With
it they can connect as that cluster's agent and displace the real one. They
still cannot reach the cluster's API server — the tunnel runs the other way —
but they would receive the traffic kubemg sends that cluster, including
request bodies and the keystrokes of any `exec` session, and could answer it
with forged responses.

**Bounds.**

- **The real install fails.** The legitimate `kubectl apply` finds its ticket
  already spent, which is the first sign.
- **A takeover is recorded.** Every time a new connection displaces a live
  agent, the audit trail records it as `agent-displaced` with both
  connections' addresses and versions, and an alarm can fire on it. See
  [When a connection displaces the agent](../clusters/agent.md#when-a-connection-displaces-the-agent).
- **The credential can be rotated.** Rotating it from the cluster's dashboard
  cuts the impostor off at once and refuses the old credential at every
  handshake after. See
  [Rotating the registration token](../clusters/agent.md#rotating-the-registration-token).

## A leaked kubeconfig

**Agent mode.** The file carries a kubemg token that is valid only against
one cluster's proxy route. It cannot reach the console's API, the audit trail
or any other cluster. It acts as its holder, with its holder's grant and
namespace scope, under every guardrail, and every call it makes is in the
audit trail under that person's name. It is bounded three ways:

- **Revocation lands on the next call.** Revoking the file from
  [the register of issued credentials](../access/kubeconfigs.md#the-register-of-issued-credentials)
  refuses it at once on the replica that served the revoke, and on the others
  within 30 seconds.
- **The grant is re-read on every call.** Disabling the account or removing
  the grant takes effect on the file's next request; the file cannot carry
  more than the person currently holds.
- **It expires.** Its lifetime is capped by the administrator's
  [kubeconfig lifetime setting](../access/kubeconfigs.md#the-ttl-ladder-and-the-two-ceilings).

**Direct mode.** The file carries a token the cluster minted itself. kubemg
cannot withdraw it: it works until its own expiry, however the grant changes
in the meantime. The only lever is on the cluster, deleting that person's
ServiceAccount, which invalidates every direct-mode file they hold for that
cluster. This is the sharpest practical difference between the two modes —
see [Revocation differs by mode](../access/kubeconfigs.md#revocation-differs-by-mode).

## A leaked machine token

**Reaches.** The clusters and namespaces its machine account is granted, in
agent mode only, with the same guardrails and audit trail as a person.

**Bounds.** Only the token's hash is stored, so the database never held a
usable copy. Revoking it takes effect on its next use. It is refused outright
against a direct-mode cluster, against a cluster the account has no grant on,
and against a namespace outside that grant. A token issued without an expiry
is the case to watch — that choice must be asked for explicitly, and its
last-used time is what to review it against. See [Machine accounts](../access/machine-accounts.md).

## A malicious or renamed IdP identity

Two different incidents share this heading: a person who can edit their own
profile at the identity provider, and a provider that is itself compromised.

**A renamed identity.** Many providers let a person change their own
`preferred_username`, `nickname` or email. kubemg bounds what that is worth:

- **A username cannot name a cluster identity.** kubemg asserts every account
  to the cluster as `kubemg:u:<username>`, and refuses a username containing
  `:` at sign-in rather than rewriting it. Renaming yourself
  `system:serviceaccount:kube-system:…` or `system:admin` gets you refused,
  not that identity.
- **A username cannot take over an account.** An existing account is found by
  the provider's own subject id first. An account that matches only by name —
  a local one, one owned by another provider, or one in the same provider
  whose recorded id is different — is refused, not signed into.
- **Pin the claim.** The SSO settings warn when the username claim is one the
  provider lets people edit. Use `sub`, or an attribute only a directory
  administrator writes. See [SSO](../access/sso.md).

**A compromised provider.** A provider can vouch for anyone it likes, so it
reaches whatever its [group mappings](../access/sso.md#group-mappings) grant —
including the `admin` system role, if a mapping gives that. It cannot reach
further: no mapping can grant `superadmin`, a provider never adopts a local
account or another provider's accounts, and hand-written grants are never
reconciled away by it. Keep the mappings as narrow as the provider deserves,
and keep at least one local super admin, which is the account that still
works when a provider has to be switched off.

## A compromised agent

**Reaches.** An attacker running code as the agent holds its ServiceAccount,
and so its impersonation grant: `kubemg:cluster-admin` on that one cluster,
and any user name. It also sees that cluster's share of tunnel traffic. It
cannot reach any other cluster, and it cannot reach the bastion's database or
any other agent.

**Bounds.** The narrowed grant keeps `system:masters` and ServiceAccounts out
of reach, and the cluster's own audit log names the agent's ServiceAccount on
every impersonated call. The agent runs as a non-root container with a read-only
filesystem and every capability dropped, runs no controllers, and listens on
nothing but its own health probes, which is what keeps this scenario rare — but when it happens, treat it as cluster-admin on that cluster.
