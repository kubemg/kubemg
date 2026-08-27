# Security model

What is trusted where, and what limits a compromise of each part. Read this
before putting kubemg in front of anything that matters — [connection modes](../clusters/connection-modes.md),
[the access model](../access/model.md) and [command guardrails](../access/guardrails.md)
cover the same ground in more operational detail; this page is the threat
picture that motivates them.

## Trust boundaries and what is stored where

| Held by | What | Notes |
| --- | --- | --- |
| kubemg's Postgres | Users, groups, grants, cluster registrations, settings, audit records, session-recording metadata | Direct-mode clusters additionally have their `service_account_token` here — a real, standing cluster credential. Agent-mode clusters have only a registration token. |
| A generated kubeconfig (agent mode) | A kubemg-issued JWT scoped to one cluster's proxy route (`auth.ScopeProxy`), plus the bastion's CA if it is self-signed | Never a cluster-native credential. |
| A generated kubeconfig (direct mode) | A short-lived token minted straight from the target cluster's own TokenRequest API | A real cluster credential, on a laptop. |
| A machine account | A `kmgm_`-prefixed opaque secret; only its SHA-256 hash is stored | Revocation is a database write, effective on the token's next use — not a wait for expiry. |
| The agent | Its cluster registration token (`kmg_`-prefixed) and, if the bastion is self-signed, the bastion's CA certificate | Nothing else; it holds no session, no user identity, no long-lived cluster credential of its own beyond the service account it already runs as. |

## Agent mode stores no cluster credential

In agent mode, kubemg's database holds only the registration token the agent
presented when it dialled in — nothing that would let kubemg (or anyone who
stole kubemg's database) reach the cluster directly. The agent is the one
holding a Kubernetes service account token, and it only ever uses it to talk
to its own local API server. Compare direct mode, where kubemg stores a real
service account token for the target cluster in its own database — a
strictly larger blast radius if that database is ever read.

## Impersonation instead of per-user service accounts

The proxy never creates or manages a Kubernetes credential per user. Every
call is forwarded with `Impersonate-User: <username>` and
`Impersonate-Group: kubemg:<role>, kubemg:users`, and the cluster's own RBAC
— through the `kubemg:view`/`kubemg:edit`/`kubemg:cluster-admin`
ClusterRoleBindings the agent manifests install — decides what that identity
may actually do. A `view` grant is read-only because the cluster says so, not
because kubemg remembered to check on every code path. Client-supplied
`Authorization` and `Impersonate-*` headers on the incoming request are
stripped before kubemg's own are set, so nothing a caller sends can widen
what it is impersonated as.

## The confined proxy-scoped JWT

A generated kubeconfig lives on a laptop, potentially for weeks, so the token
inside it is deliberately not a general session credential. It carries
`Scope: "proxy"` and a `ClusterID`, and `RequireAuth` enforces both: the token
is only valid against `/api/v1/clusters/:id/proxy/*path` for that one cluster
ID, matched against the request's *registered route* rather than the raw
URL. A stolen file cannot be replayed against the users API, the audit trail,
or any other cluster's proxy — see [the worked threat note](#a-stolen-kubeconfig-agent-mode)
below.

## Namespace scope vs. role: enforced in two different places, on purpose

A grant's **role** (`view`/`edit`/`cluster-admin`) is deliberately *not*
re-checked locally — it is resolved into an impersonation group and handed to
the cluster, whose RBAC is the one place that already has to get "may `view`
write" right. Duplicating that decision inside kubemg would only create a
second place for the two to disagree.

A grant's **namespace scope** has no equivalent in Kubernetes impersonation —
there is no `Impersonate-Group` that means "only these three namespaces" — so
it is enforced by the proxy itself, on every call, before anything reaches
the tunnel. A scoped grant is refused outright on a request that names no
namespace or one outside its list, discovery paths excepted.

## The direct-mode gap, stated plainly

In **direct** connection mode, kubemg mints tokens through TokenRequest but
provisions **no RoleBinding** for them. A generated kubeconfig there
authenticates against the cluster without the cluster having any opinion on
what that identity may do — whatever the stored service account was already
bound to is what a caller gets, and the permission matrix governs *kubemg's
own* authorization rather than the target cluster's RBAC. This is exactly why
[machine accounts](../access/machine-accounts.md) refuse direct-mode clusters
outright: a credential kubemg cannot see authorized on the cluster is not one
it should hand out for unattended, months-long use. Agent mode is where this
closes, because impersonation plus the installed ClusterRoleBindings put the
decision back with the cluster. The cluster detail page, the permissions
page and the registration wizard's last step all disclose which mode a given
cluster is in — this is treated as load-bearing, not a decoration.

## What is redacted, and what never leaves the server

- **ConfigMap and Secret listings return keys only.** No value ever enters a
  response, so nothing lands in a browser cache, a browser history entry, or
  a log line just because someone opened a list.
- **One Secret value can be revealed, and only under its own capability.**
  `GET .../resources/secret/value?name=&key=` returns one key of one Secret.
  It exists because the alternative was not "the value stays in the cluster" —
  it was an operator running `kubectl get secret -o jsonpath`, where the reveal
  happens with no record at all. It needs `can_reveal_secrets` on the account,
  which only a super admin may grant (so an administrator cannot grant it to
  itself), *and* the cluster's own RBAC on the impersonated read. It is
  recorded under its own audit verb, naming the caller, the Secret and the key,
  **before the value is written**, and no audit selection can suppress it. A
  ServiceAccount token and KubeMG's own agent registration secret are refused
  outright, and nothing caches the response at any layer. An install that does
  not want this grants the capability to nobody.
- **Helm's rendered manifest never leaves the server.** A release's stored
  object also carries the chart's fully rendered manifest, which for many
  charts holds generated passwords — only chart metadata and `values` are
  returned. Writing new values appends a revision rather than templating a
  new manifest, and that caveat travels with both the read and the write.
- **The core Kubernetes API group is refused on the custom-resource route.**
  `GET .../resources/custom` lets a caller name any `group/version/plural`
  to read a CRD kubemg does not know about first-class — but the core group
  (no dot in its name) is refused outright, because that is where Secrets
  live and their lists are served by handlers that redact first. Naming an
  API is not the same as reaching one, and this route must never become the
  way around the redaction above.

## Recordings: the most sensitive artefact kubemg writes

A session recording is a transcript of everything typed into and printed
from a production shell — passwords a prompt never echoed included, if
keystroke capture is on. Four controls follow from that:

- **Encrypted at rest**, chunked AES-256-GCM rather than one seal over the
  whole file, so a recording that is truncated, reordered or altered fails
  to decrypt rather than replaying short.
- **Keystrokes are optional** (`KUBEMG_SESSION_RECORDING_INPUT=false`) — a
  pty already echoes what was typed, so dropping input capture loses
  precisely the part a prompt refuses to echo, which is the part worth not
  storing.
- **Watching a recording is itself audited**, before the bytes go out, with
  the viewer and the subject's session recorded as two different identities
  — "who watched whose shell" needs both halves to answer.
- **Reaching someone else's recording is a capability of its own**
  (`CanViewRecordings`), separate from the admin role and grantable only by
  a super admin — an administrator cannot grant it to themselves, which is
  what keeps the control from being theatre.

See [Session recording](../audit/session-recording.md) for the full mechanism.

## Audit floors nothing suppresses

The audit trail can be narrowed to fewer verbs on a busy fleet, but three
things are never suppressed regardless of that setting: **any refusal or
error**, **any streaming call**, and kubemg's own `replay`/
`recording-get`/`recording-delete` actions. An empty selection means "record
every verb again," never "record nothing" — the floor holds even then. See
[Audit trail](../audit/trail.md).

## Threat notes

### A stolen kubeconfig (agent mode)

The file carries a proxy-scoped JWT
good for exactly one cluster's proxy route. It cannot reach the users API,
the audit trail, or any other cluster. Revoking the underlying grant takes
effect on the file's very next call, because the proxy re-reads the user and
the grant on every request rather than trusting what the token claimed at
mint time.

### A stolen kubeconfig (direct mode)

The file carries a real,
cluster-minted token. It works until that token's own expiry, however the
grant changes in the meantime — there is no tunnel re-check to cut it off
early. This is the sharpest practical difference between the two modes.

### A stolen machine token

The secret is never stored in the clear — only
its SHA-256 hash — so a database compromise does not hand over a usable
credential retroactively (though a live secret in a CI store is a live
credential regardless). Revoking it is a row write, effective on its next
use. It is refused outright against a direct-mode cluster, against a cluster
the account holds no grant on, and against any namespace outside that grant.

### A compromised agent

The agent holds no cluster-admin credential of its
own beyond whatever service account it runs as, and that service account's
ClusterRole grants exactly one privilege: `impersonate` on users and groups.
It makes no authorization decisions — a compromised agent can forward
whatever the bastion sends it, but the bastion only sends what a caller's
grant, namespace scope and guardrails already allowed, and the *cluster's*
RBAC is still the one deciding what the impersonated identity may do once
the call lands.

### A compromised bastion

This is the highest-value target, by design and
without disguising it: the bastion holds every grant, mints every
impersonation header, and terminates the tunnel every agent trusts. A
compromise here is a compromise of the fleet's access model, which is why
the manual's install guidance treats the bastion's own host, database and
TLS material as the thing to harden hardest — see the
[production checklist](../install/production-checklist.md).
