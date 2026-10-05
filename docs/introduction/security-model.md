# Security model

What is trusted where, and what limits a compromise of each part. Read this
before putting kubemg in front of anything that matters. For what an attacker
reaches in each incident, see the [threat model](threat-model.md); for the
operational detail, see [connection modes](../clusters/connection-modes.md),
[the access model](../access/model.md) and [command guardrails](../access/guardrails.md).

## Trust boundaries and what is stored where

| Held by | What | Notes |
| --- | --- | --- |
| kubemg's Postgres | Users, groups, grants, cluster registrations, settings, audit records, session-recording metadata | Direct-mode clusters also have their `service_account_token` here, a real standing cluster credential. Agent-mode clusters have only a registration token. Every stored credential is encrypted under `KUBEMG_SECRET_KEY` when one is set; see [the database is the crown jewel](#the-database-is-the-crown-jewel). |
| A generated kubeconfig (agent mode) | A kubemg-issued JWT scoped to one cluster's proxy route, plus the bastion's CA if it is self-signed | Never a cluster-native credential. |
| A generated kubeconfig (direct mode) | A short-lived token minted by the target cluster's own TokenRequest API | A real cluster credential, on a laptop. |
| A machine account | A `kmgm_`-prefixed opaque secret; only its SHA-256 hash is stored | Revocation is a database write, effective on the next use. |
| The agent | Its registration token (`kmg_`-prefixed) and, if the bastion is self-signed, the bastion's CA certificate | No session, no user identity, no long-lived cluster credential beyond the service account it already runs as. |
| An install URL | A `kmgi_`-prefixed single-use download ticket; only its SHA-256 hash is stored | Spent by the first download, expires after 15 minutes. It is **not** the registration token; the package it downloads carries that. |

## Agent mode stores no Kubernetes credential, and what that does not mean

In agent mode the database holds only the registration token the agent presents
when it dials in. Someone who steals the database cannot call the cluster's API
server with it. Direct mode stores a real service account token instead, a
strictly larger blast radius.

That is true, and on its own misleading. The agent's service account may
**impersonate**, and the agent forwards whatever the bastion sends. Its grant is
narrowed to kubemg's own four groups, so it cannot claim `system:masters`, but
one of those groups is bound to `cluster-admin` and the user name can be any
name. **The bastion plus the tunnel is, in effect, `system:masters` on every
agent-mode cluster.** The [threat model](threat-model.md#what-the-bastion-is)
says what bounds it.

## The database is the crown jewel

The generated signing key signs every session and every agent-mode kubeconfig.
Anyone who can read it can mint a super-admin token, which through the tunnel is
cluster-admin on every agent-mode cluster. The agent registration tokens beside
it are every agent's identity.

- With `KUBEMG_SECRET_KEY` set, both and every other stored credential are
  AES-256-GCM ciphertext under a key held in the server's environment. A stolen
  dump, replica or backup is no longer the keys to the fleet.
- The server refuses to start over ciphertext it cannot open rather than guess.
- Setting `JWT_SECRET` as well keeps the signing key out of the database.

What is encrypted, and what losing the key costs, is on the
[Database](../install/database.md#credentials-encrypted-at-rest) page.

## The agent's registration token

Whoever holds the registration token can be that cluster's agent: receive the
traffic the bastion sends down the tunnel and answer it. They cannot reach the
cluster's API server, but they would see request bodies and exec keystrokes.
Three things bound that:

- **The install URL is not the token.** It carries a single-use download ticket
  that dies after the first download or 15 minutes. A URL left in shell history
  or a CI log is not a credential. Older URLs answer `410 Gone`.
- **The token can be rotated** from the cluster's dashboard. The attached agent
  is disconnected at once, the old token is refused at every later handshake,
  and the agent stays down until the new package is applied. Audited as
  `agent-token-rotate`.
- **A takeover is recorded.** A cluster has one tunnel and the newest
  connection wins, which a rolling Deployment needs and an impostor would also
  do. Every takeover is audited as `agent-displaced` and can drive an alarm. See
  [When a connection displaces the agent](../clusters/agent.md#when-a-connection-displaces-the-agent).

## Impersonation instead of per-user service accounts

The proxy never creates a Kubernetes credential per user. Every call is
forwarded with `Impersonate-User: kubemg:u:<username>` and
`Impersonate-Group: kubemg:<role>, kubemg:users`. The cluster's own RBAC,
through the `kubemg:view`/`kubemg:edit`/`kubemg:cluster-admin` bindings the
agent manifests install, decides what that identity may do. A `view` grant is
read-only because the cluster says so. Any `Authorization` or `Impersonate-*`
header the client sends is stripped first, so a caller cannot widen what it is
impersonated as.

The `kubemg:u:` prefix stops a *username* from widening it: an account named
like a ServiceAccount or a `system:` identity would otherwise be that identity
to the cluster. Usernames containing `:` are refused, and the agent may
impersonate only kubemg's four groups and no ServiceAccount. See
[Why the username is prefixed](../access/model.md#why-the-username-is-prefixed).

## The confined proxy-scoped JWT

A generated kubeconfig lives on a laptop, possibly for weeks, so its token is
not a general session credential. It is valid only against that one cluster's
proxy route, matched against the request's registered route rather than the raw
URL. A stolen file cannot be replayed against the users API, the audit trail or
any other cluster. See [a stolen kubeconfig](threat-model.md#a-leaked-kubeconfig).

## Namespace scope vs. role: enforced in two different places, on purpose

??? info "Why it works this way"
    A grant's **role** is resolved into an impersonation group and handed to
    the cluster, whose RBAC already has to get "may `view` write" right.
    Duplicating that inside kubemg would only create a second place to disagree.

    A grant's **namespace scope** has no Kubernetes impersonation equivalent
    (no group means "only these three namespaces"), so the proxy enforces it
    itself, on every call, before anything reaches the tunnel.

| Part of a grant | Enforced by |
| --- | --- |
| Role (`view`/`edit`/`cluster-admin`) | The cluster's own RBAC, not re-checked locally |
| Namespace scope | The proxy. A scoped grant is refused on a request that names no namespace or one outside its list, discovery paths excepted |

## The direct-mode gap, stated plainly

In **direct** mode kubemg mints tokens through TokenRequest but provisions **no
RoleBinding** for them. A generated kubeconfig authenticates against the cluster
without the cluster having any opinion on what that identity may do. Whatever
the stored service account was already bound to is what a caller gets, and the
permission matrix governs kubemg's own authorization, not the target cluster's
RBAC.

- This is why [machine accounts](../access/machine-accounts.md) refuse
  direct-mode clusters outright.
- Agent mode closes the gap: impersonation plus the installed bindings put the
  decision back with the cluster.
- The cluster detail page, the permissions page and the registration wizard's
  last step all say which mode a cluster is in.

## What is redacted, and what never leaves the server

- **ConfigMap and Secret listings return keys only.** No value enters a list
  response, so none lands in a browser cache, history or log line.
- **One Secret value can be revealed, only under its own capability.** It needs
  `can_reveal_secrets` on the account, which only a super admin may grant (an
  administrator cannot grant it to themselves), *and* the cluster's own RBAC on
  the impersonated read. It is audited under its own verb, naming the caller,
  Secret and key, **before the value is written**, and no audit selection can
  suppress it. ServiceAccount tokens and kubemg's own agent registration secret
  are refused, and nothing caches the response. An install that does not want
  this grants the capability to nobody.
- **Helm's rendered manifest never leaves the server.** It can hold generated
  passwords; only chart metadata and `values` are returned.
- **The core Kubernetes API group is refused on the custom-resource route.**
  That is where Secrets live, and their lists are redacted elsewhere; this route
  must never become the way around it.

??? info "Why the reveal exists at all"
    The alternative was not "the value stays in the cluster". It was an operator
    running `kubectl get secret -o jsonpath`, where the reveal happens with no
    record at all.

## Recordings: the most sensitive artefact kubemg writes

A session recording is a transcript of everything typed into and printed from a
production shell, including passwords a prompt never echoed if keystroke
capture is on. Four controls follow:

- **Encrypted at rest** in chunks, so a truncated, reordered or altered
  recording fails to decrypt rather than replaying short.
- **Keystrokes are optional** (`KUBEMG_SESSION_RECORDING_INPUT=false`). A pty
  already echoes what was typed, so dropping input loses only the part a prompt
  refuses to echo, which is the part worth not storing.
- **Watching a recording is itself audited**, before the bytes go out, with the
  viewer and the session's owner recorded as two identities.
- **Reaching someone else's recording is its own capability**, separate from
  the admin role and grantable only by a super admin.

See [Session recording](../audit/session-recording.md).

## Audit floors nothing suppresses

The audit trail can be narrowed to fewer verbs, but three things are never
suppressed: **any refusal or error**, **any streaming call**, and kubemg's own
`replay`/`recording-get`/`recording-delete` actions. An empty selection means
"record every verb again", never "record nothing". See
[Audit trail](../audit/trail.md).

## Threat notes

Each scenario is worked through in the [threat model](threat-model.md). The
short form:

### A stolen kubeconfig (agent mode)

Valid for one cluster's proxy route only, and cut off on its next call once the
grant is revoked. See [A leaked kubeconfig](threat-model.md#a-leaked-kubeconfig).

### A stolen kubeconfig (direct mode)

A real cluster-minted token that works until its own expiry, however the grant
changes. This is the sharpest difference between the two modes. See
[A leaked kubeconfig](threat-model.md#a-leaked-kubeconfig).

### A stolen machine token

Only its hash is stored; revoking it takes effect on the next use; it is
refused against direct-mode clusters. See
[A leaked machine token](threat-model.md#a-leaked-machine-token).

### A compromised agent

Treat it as cluster-admin on that one cluster, and nothing else. See
[A compromised agent](threat-model.md#a-compromised-agent).

### A compromised bastion

The highest-value target, by design: in effect `system:masters` on every
agent-mode cluster. What bounds it is outside it: off-host audit forwarding, the
cluster's own audit log, and removing the agent. See
[A compromised bastion](threat-model.md#a-compromised-bastion) and the
[production checklist](../install/production-checklist.md).
