# Kubeconfigs

A kubeconfig has no page of its own in kubemg — it is generated from a sheet opened off a cluster's page (`KubeconfigDrawer`, `POST /api/v1/clusters/:id/kubeconfig/generate`). This page covers what that file contains, how long it can live, and what revoking it actually does — which differs by [connection mode](../clusters/connection-modes.md).

## Generating one

```json title="POST /api/v1/clusters/:id/kubeconfig/generate"
{ "ttl_seconds": 3600, "namespace": "team-a" }
```

Both fields are optional. `ttl_seconds` defaults to `k8s.DefaultTTL` (1 hour); `namespace` defaults to the first namespace in the caller's grant, or `default` for an unscoped grant. A `namespace` outside the caller's granted scope is refused with `403 namespace is outside your granted scope` — the same enforcement point every namespace-scoped read and write goes through.

The response:

```json title="200 OK"
{
  "cluster": "prod-eu",
  "context": "prod-eu",
  "namespace": "team-a",
  "ttl_seconds": 3600,
  "expires_at": "2026-08-25T15:00:00Z",
  "filename": "prod-eu-ada.kubeconfig",
  "kubeconfig": "apiVersion: v1\n...",
  "k8s_role": "edit",
  "service_account": "",
  "connection_mode": "agent",
  "server": "https://kubemg.example.com/api/v1/clusters/3/proxy",
  "warning": ""
}
```

## The TTL ladder and the two ceilings

There are deliberately **two** ceilings, defined in `pkg/k8s`:

```go
const (
    MinTTL        = 10 * time.Minute
    DefaultMaxTTL = 24 * time.Hour
    MaxTTL        = 90 * 24 * time.Hour
    DefaultTTL    = time.Hour
)
```

- **`DefaultMaxTTL` (24 hours)** is what an install allows when nobody has said otherwise — a credential sitting on a laptop for longer than a day is the exposure kubemg cannot see being used.
- **`MaxTTL` (90 days / one quarter)** is the *absolute* bound. No administrator setting can push past it: beyond a quarter, a bearer token stops being access control and becomes a permanent key. `pkg/k8s` enforces only this absolute bound — it is the minting layer, not the policy layer.

The effective ceiling in between is a runtime setting, `kubeconfig_max_ttl_hours`, resolved in the API layer (`s.kubeconfigMaxTTL`):

```go
func (s *server) kubeconfigMaxTTL(ctx context.Context) time.Duration {
    hours := s.settings(ctx).KubeconfigMaxTTLHours
    ceiling := time.Duration(hours) * time.Hour
    if ceiling < time.Hour || ceiling > k8s.MaxTTL {
        return k8s.DefaultMaxTTL
    }
    return ceiling
}
```

Stored in **hours**, deliberately — a value that has to move both directions (an install handing out a quarter, and one refusing anything past an eight-hour shift) cannot be expressed in whole days. An out-of-bounds stored value — including `0` — reads as unset and falls back to `DefaultMaxTTL`, the same rule the audit retention window follows: a ceiling read wrong is either every request refused or a credential longer than this build will ever sign for, so an ambiguous value defaults to the safer failure.

`kubeconfig_max_ttl_hours` is set from **Admin → Settings**, as a plain number field rather than a preset ladder — a ceiling is an administrator's one-time decision, not a per-request choice.

### The policy endpoint

```
GET /api/v1/kubeconfig/policy
```

Readable by **any authenticated caller**, not just admins — the same rule the recording policy follows: a form offering a choice must not discover the ceiling by being refused.

```json title="200 OK"
{ "min_ttl_seconds": 600, "default_ttl_seconds": 3600, "max_ttl_seconds": 86400 }
```

The console's own generator sheet filters a fixed preset ladder (1h → 90d) against `max_ttl_seconds` rather than offering a free-typed number box — a text field invites 480 [minutes when hours were meant, or the reverse]. Raising the effective ceiling past a day is disclosed both in the Settings warnings and again in the sheet, because agent mode and direct mode differ on the one thing that matters about a long-lived credential — see [Revocation](#revocation-differs-by-mode) below.

## What the file contains, by connection mode

### Agent mode

The target cluster has no route kubemg can dial directly, and no cluster credential is stored at all — so the kubeconfig cannot point at the cluster. Instead:

- `server` is `{public_url}/api/v1/clusters/:id/proxy` — kubemg's own proxy.
- The bearer token is a **kubemg-issued JWT minted with `auth.ScopeProxy` and the cluster's id** (`GenerateProxyToken`), not a Kubernetes credential at all.
- `RequireAuth` confines a proxy-scoped token to exactly that cluster's `/proxy` route — a kubeconfig lives on a laptop, so it must never double as a session key for the rest of the kubemg API.
- `certificate-authority-data` carries **the bastion's own CA**, when the bastion has one pinned (self-signed or an operator-supplied `KUBEMG_AGENT_CA_BUNDLE`) — because the "cluster" kubectl is dialing is kubemg itself, so the CA it has to trust is kubemg's, not the target cluster's. A publicly-trusted bastion certificate embeds nothing, because pinning it would break the file at the next certificate renewal.

### Direct mode

kubemg holds a stored API URL and service account token for the cluster and dials it directly:

- `server` is the cluster's own `api_url`.
- The bearer token is minted **on the target cluster** via the Kubernetes `TokenRequest` API, for a service account named after the caller's username (`k8s.ServiceAccountName`).
- `certificate-authority-data` is the cluster's own stored CA certificate.
- `service_account` in the response names the in-cluster identity the token authenticates as; this field is empty in agent mode, where there is no service account involved at all — the caller is impersonated instead.

## The granted-vs-requested TTL warning

A cluster's own API server may enforce `--service-account-max-token-expiration`, and it answers a request for a longer window with an **earlier expiry rather than an error** — silently, from kubemg's point of view. Reporting the TTL that was *asked for* would have the console counting down from time the token was never actually issued for, so the response reports what the cluster **granted**:

```go
// The cluster's own API server caps service account tokens ... and answers a
// longer request with an earlier expiry rather than an error. Reporting the
// TTL that was asked for would make the console count down from a window the
// token does not have, so what is reported is the window the cluster granted.
granted, shortened := grantedTTL(ttl, issued.ExpiresAt)
```

When the cluster shortened the window, `warning` names the specific mechanism:

```
This cluster's API server caps service account tokens at about 1 hour, so it issued 1 hour instead
of the 1 day that was requested. Raising it means raising the API server's own
--service-account-max-token-expiration, or registering the cluster in agent mode, where the
credential is kubemg's rather than the cluster's.
```

This warning only applies to **direct mode** — an agent-mode credential is kubemg's own JWT, unaffected by anything the target cluster's API server enforces.

## The plain-HTTP refusal warning

`client-go` refuses to send a bearer token over plain `http://` at all, even to loopback. A kubeconfig generated while kubemg's public URL is not HTTPS still renders — refusing to generate the file would be worse than handing over one that needs a fix — but carries a warning naming the problem:

```
This server's public URL is not HTTPS. kubectl refuses to send a bearer token over plain HTTP,
so put TLS in front of kubemg before using this kubeconfig.
```

See [TLS](../install/tls.md) for terminating TLS at the bastion.

## The register of issued credentials

Every generated kubeconfig writes a row (`kubeconfig_issuances`): who holds it, which cluster, which mode, the namespace and role, when it was issued and by whom, when it expires, and when it was last used. The generator writes it itself, and the same act lands in the audit trail under `kubeconfig-issue` with the identities crossed — the record's user is whoever asked, and the holder is named as the impersonated identity, because an administrator generating a file for somebody else is exactly the row an auditor is looking for and neither half says it alone.

The register exists because **revocation needs it first**: you cannot withdraw what was never recorded, and until this table existed generating a kubeconfig was the one act in kubemg that handed out a credential and wrote nothing down.

```
GET  /api/v1/kubeconfigs
POST /api/v1/kubeconfigs/:id/revoke
POST /api/v1/kubeconfigs/revoke-all
```

Reading follows the audit trail's rule exactly: everybody may read, a non-admin is narrowed by the handler to their own rows, and the `user_id` query parameter can narrow that further but **never widen it**. Revoking your own credential is never administrative — revoking a file you know you lost must not require finding an administrator — and revoking somebody else's always is. In the console the register is **Admin → Identity → Issued credentials** for the fleet, and `/me/credentials` for an operator's own.

`last_used_at` is written off the request's own path and at most once every five minutes per credential (`pkg/credentials`), the machine token's rule: this read would otherwise sit in front of every proxied call. A credential that was generated and never used is the most useful row on the page.

Retention follows the audit window, and a revoked or expired row is **kept rather than deleted** — "what existed and when did it stop" is the question a register answers.

## Revocation differs by mode

- **Agent mode**: the credential is a proxy-scoped kubemg JWT, and revoking it is a lookup the token cannot argue with — the `jti` in its claims against the register. That set is a **published immutable snapshot** (`pkg/credentials`, shaped exactly like `pkg/auditpolicy`): the HTTP layer republishes on every revoke, the gateway reads it lock-free in `Proxy`'s authorize step, and the next call the file makes gets `401`. A server whose register cannot be read **fails open on nothing** — an unreadable register means no revocations are known, never that every token is refused, because a blip that locks a whole fleet out of `kubectl` is worse than one that briefly honours a withdrawn file. Replicas agree within 30 seconds; the replica that served the revoke is immediate.
- **Direct mode**: the token is minted *on the cluster* via TokenRequest. kubemg cannot withdraw what it did not mint — it keeps working, exactly as issued, until its own expiry passes. The per-row Revoke is therefore **offered in agent mode only**, and a direct-mode row says why rather than showing a button that would report success and change nothing. The one real lever is cluster-side: deleting the per-user `kubemg-<username>` ServiceAccount invalidates every token bound to it, and it is inherently **all-or-nothing per cluster**, since every one of that user's direct-mode kubeconfigs on that cluster is bound to the same account. It is also not instant — the API server caches a successful authentication for about ten seconds, so a token keeps working for that long after the account is gone. Verified end-to-end against a real cluster: the call answers `403` (the token authenticates; direct mode provisions no RoleBinding) until the cache expires, and `401` from then on.

Two levers that already existed remain the fastest blunt instruments and are unchanged: **disabling the account** and **revoking the grant** both take effect on the very next call, because the proxy re-reads the user row and the grant every time (`bastion/proxy.go`). What the register adds is the lever for the case those two are wrong for — the laptop was stolen, and the person still works here and still needs their console.

### Revoking everything one person holds

`POST /api/v1/kubeconfigs/revoke-all` is its own action rather than *N* row writes, because it is what an incident calls for. It states what it actually reached:

```json title="200 OK"
{
  "revoked": 3,
  "still_valid": 1,
  "clusters_not_reached": ["prod-eu"],
  "explanation": "These clusters are registered for direct API access, so their credentials are tokens the clusters themselves minted…"
}
```

Refusing to disclose that difference would be worse than not shipping the button: an administrator who believes a revoke landed when it did not is in a worse position than one who knows the token has four more hours to run. The failed half is recorded as such in the audit trail too — each withdrawal is its own `kubeconfig-revoke` record, and a direct-mode one carries the reason it did not land.

### Rotating a password can take them with it

`POST /api/v1/auth/password` is where somebody changes their **own** password, and it accepts `revoke_kubeconfigs: true`. That is the same blanket revoke, run for the caller's own account, and it returns the same summary under `credentials`:

```json title="200 OK"
{
  "changed": true,
  "credentials": { "revoked": 2, "still_valid": 0 }
}
```

It is offered rather than done. Somebody rotating a password because they think it leaked wants the kubeconfigs gone with it; somebody rotating one on a schedule does not want every laptop on their team to stop working, and doing it silently would be the wrong answer to both. The trail records both acts separately — one `password-change`, and one `kubeconfig-revoke` per credential.

In the console it is **Change password** on `/me/credentials`, beside the register it acts on. The button is absent for an account whose password is not held here: a federated account changes it with its provider, and a machine account has none at all — its credential is a [machine token](machine-accounts.md), with a revoke of its own.

## Embedded CA rules, summarized

| Mode | `certificate-authority-data` |
| --- | --- |
| Agent, bastion self-signed or custom CA configured | The bastion's own CA (`KUBEMG_AGENT_CA_BUNDLE` or the generated self-signed cert) |
| Agent, bastion publicly trusted | Empty — the system trust store already covers it, and pinning would break the file at renewal |
| Direct | The target cluster's own stored CA certificate |

See [Registering a cluster](../clusters/registering.md) and [Machine accounts](machine-accounts.md) for the equivalent kubeconfig rendered for a programmatic caller rather than a person's session.
