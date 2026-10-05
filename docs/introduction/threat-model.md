# Threat model

What an attacker reaches when each part of kubemg falls into the wrong hands,
and what bounds it. Use it to decide what to harden first. The
[security model](security-model.md) explains the controls one at a time; this
page starts from the incident.

## What the bastion is

In agent mode kubemg keeps no Kubernetes credential in its database, but that is
not the whole picture. The agent in every cluster may **impersonate**, and
forwards whatever the bastion sends down the tunnel. Its grant is as narrow as
Kubernetes allows: only kubemg's four groups, never `system:masters` or a
ServiceAccount. But one of those groups, `kubemg:cluster-admin`, is bound to
`cluster-admin`, and the user name can be anything.

**The bastion plus the tunnel is, in effect, `system:masters` on every
agent-mode cluster.** Whoever controls the bastion process controls every
agent-mode cluster attached to it. Harden the bastion, its database and its
signing key hardest; everything a user carries is bounded by the bastion
re-checking it on every call.

??? info "Why it works this way"
    This is the trust model of every central Kubernetes access product that
    reaches clusters through an in-cluster agent: Rancher's
    `cattle-cluster-agent` is bound to full control of the cluster, and
    Teleport's Kubernetes Service impersonates users and groups on the proxy's
    behalf. A central point that can grant anyone access can, by construction,
    grant itself that access. Products differ in how much runs inside the
    cluster, how narrow the grant is, and how much of the central point's work
    is written down where it cannot erase it. For kubemg: one small Deployment
    with no controllers and no CRDs, a grant limited to four named groups, and
    an audit trail that can be [forwarded off the host](../audit/trail.md) as
    it is written.

## At a glance

| Scenario | What it reaches | What bounds it |
| --- | --- | --- |
| [A compromised bastion](#a-compromised-bastion) | Every agent-mode cluster, as cluster-admin; every direct-mode cluster, as its stored ServiceAccount | Nothing inside the cluster. Off-host audit forwarding, the cluster's own audit log, and removing the agent |
| [A read of the database](#a-read-of-the-database) | With no `KUBEMG_SECRET_KEY`: the signing key, and through it the whole fleet. With one: grants, users and audit history, but no usable credential | `KUBEMG_SECRET_KEY`, and `JWT_SECRET` from the environment |
| [A leaked install URL](#a-leaked-install-url) | Nothing, if the real install used it first. Otherwise the agent package, and the ability to stand in for that cluster's agent | Single-use, 15-minute download ticket; takeover audit; token rotation |
| [A leaked kubeconfig](#a-leaked-kubeconfig) | Agent mode: one cluster, as its holder, until revoked or expired. Direct mode: one cluster, until the token expires | Revocation, grant re-check on every call, the kubeconfig lifetime ceiling |
| [A leaked machine token](#a-leaked-machine-token) | The clusters and namespaces its account is granted, agent mode only | Hashed storage, revocation on next use, no direct-mode reach |
| [A malicious or renamed IdP identity](#a-malicious-or-renamed-idp-identity) | What that provider's group mappings grant; never a local account, another provider's, or a cluster identity outside kubemg's own | Username rule, `kubemg:u:` prefix, account matching by the provider's own id |
| [A compromised agent](#a-compromised-agent) | The same cluster-admin reach as the bastion, on that one cluster | The agent's narrowed grant; the cluster's audit log |

## A compromised bastion

**Reaches.** Every agent-mode cluster as `cluster-admin` or any user name those
clusters have bound a role to; every direct-mode cluster as its stored
ServiceAccount; every live `exec` session; every session recording (the server
holds the key); sessions and kubeconfigs for any kubemg account; and its own
audit trail.

**Bounds.** Nothing inside the clusters, since the bastion chooses the identity
their RBAC decides on. What limits the damage is outside it:

- **Records already forwarded stay forwarded.** A [forwarder](../audit/trail.md)
  pushes each audit record off the host as it is written, so history up to the
  compromise survives.
- **The cluster keeps its own record.** Every call reaches the API server as the
  agent's ServiceAccount with the impersonated user beside it. With API server
  auditing on, the cluster records both independently of kubemg.
- **The tunnel can be cut from the cluster side.** Deleting the agent, or the
  binding that grants it `impersonate`, ends kubemg's reach at once.

The [production checklist](../install/production-checklist.md) therefore treats
the bastion's host, database and TLS material as the thing to protect first.

## A read of the database

**Reaches.** Every user, group, grant, cluster registration, setting and audit
record, and local password hashes. The rest depends on `KUBEMG_SECRET_KEY`:

- **Without it**, stored credentials are readable as written: the signing key,
  every agent's tunnel credential, direct-mode ServiceAccount tokens, and
  datasource, Helm repository, alarm channel and SSO secrets. The signing key
  alone mints a super-admin session, which is cluster-admin on every agent-mode
  cluster. **A database dump without the key is the bastion.**
- **With it**, each is AES-256-GCM ciphertext under a key that lives in the
  server's environment. A stolen dump, replica or backup is no longer a working
  credential. Machine tokens and install tickets were never stored in the clear.

`JWT_SECRET` in the environment keeps the signing key out of the database.
Session recordings are files on disk, encrypted under
`KUBEMG_SESSION_RECORDING_KEY` when set. See
[Credentials encrypted at rest](../install/database.md#credentials-encrypted-at-rest).

A **write** to the database is a different incident: grants and system roles
are rows, so whoever can write to it can grant themselves anything.

## A leaked install URL

**Reaches.** Usually nothing. The URL carries a single-use download ticket, not
the agent's credential. The first download spends it and an unused one expires
after 15 minutes, so a URL in shell history or a CI log is dead.

If someone fetches it **before** the real install, they get the package, which
carries the cluster's tunnel credential. They can connect as that cluster's
agent and displace the real one. They cannot reach the cluster's API server (the
tunnel runs the other way), but they would receive traffic kubemg sends that
cluster, including request bodies and `exec` keystrokes, and could forge
responses.

**Bounds.**

- **The real install fails.** The legitimate `kubectl apply` finds its ticket
  spent, which is the first sign.
- **A takeover is recorded** as `agent-displaced` with both connections'
  addresses and versions, and an alarm can fire on it. See
  [When a connection displaces the agent](../clusters/agent.md#when-a-connection-displaces-the-agent).
- **The credential can be rotated**, which cuts the impostor off at once. See
  [Rotating the registration token](../clusters/agent.md#rotating-the-registration-token).

## A leaked kubeconfig

**Agent mode.** The token is valid only against one cluster's proxy route. It
cannot reach the console's API, the audit trail or any other cluster. It acts as
its holder, with their grant and namespace scope, under every guardrail, and
every call is in the audit trail under their name. Three bounds:

- **Revocation lands on the next call.** Revoking from
  [the register of issued credentials](../access/kubeconfigs.md#the-register-of-issued-credentials)
  refuses it at once on the replica that served the revoke, and on the others
  within 30 seconds.
- **The grant is re-read on every call.** Disabling the account or removing the
  grant takes effect on the next request.
- **It expires.** Lifetime is capped by the
  [kubeconfig lifetime setting](../access/kubeconfigs.md#the-ttl-ladder-and-the-two-ceilings).

**Direct mode.** The token was minted by the cluster, and kubemg cannot withdraw
it. It works until its own expiry, however the grant changes. The only lever is
on the cluster: deleting that person's ServiceAccount invalidates every
direct-mode file they hold for it. This is the sharpest difference between the
modes; see [Revocation differs by mode](../access/kubeconfigs.md#revocation-differs-by-mode).

## A leaked machine token

**Reaches.** The clusters and namespaces its account is granted, in agent mode
only, with a person's guardrails and audit trail.

**Bounds.** Only the hash is stored. Revoking takes effect on the next use. It
is refused against a direct-mode cluster, a cluster with no grant, and a
namespace outside the grant. A token with no expiry must be asked for
explicitly; review it against its last-used time. See
[Machine accounts](../access/machine-accounts.md).

## A malicious or renamed IdP identity

Two incidents share this heading: a person editing their own profile at the
identity provider, and a compromised provider.

**A renamed identity.** Many providers let a person change their own
`preferred_username`, `nickname` or email.

- **A username cannot name a cluster identity.** kubemg asserts every account as
  `kubemg:u:<username>` and refuses a username containing `:` at sign-in.
  Renaming yourself `system:serviceaccount:kube-system:…` gets you refused.
- **A username cannot take over an account.** An existing account is found by
  the provider's own subject id first. A name-only match (a local account, one
  owned by another provider, or one with a different recorded id) is refused.
- **Pin the claim.** The SSO settings warn when the username claim is
  user-editable. Use `sub`, or an attribute only a directory administrator
  writes. See [SSO](../access/sso.md).

**A compromised provider.** It can vouch for anyone, so it reaches whatever its
[group mappings](../access/sso.md#group-mappings) grant, including `admin` if a
mapping says so. It cannot reach further: no mapping grants `superadmin`, a
provider never adopts a local or another provider's account, and hand-written
grants are never reconciled away by it. Keep mappings narrow and keep at least
one local super admin, the account that still works when a provider is switched
off.

## A compromised agent

**Reaches.** The agent's ServiceAccount and so its impersonation grant:
`kubemg:cluster-admin` on that one cluster, with any user name. It also sees that
cluster's tunnel traffic. It cannot reach any other cluster, the bastion's
database, or another agent.

**Bounds.** The narrowed grant keeps `system:masters` and ServiceAccounts out of
reach, and the cluster's audit log names the agent's ServiceAccount on every
impersonated call. The agent runs non-root with a read-only filesystem and every
capability dropped, has no controllers, and listens only for its health probes.
That makes this rare, but when it happens, treat it as cluster-admin on that
cluster.
