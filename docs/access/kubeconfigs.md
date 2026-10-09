# Kubeconfigs

Generate a short-lived kubeconfig for a cluster, see every one that has been issued, and revoke them. A kubeconfig has no page of its own: open the generate sheet from a cluster's page. What the file contains and what revoking does depend on the [connection mode](../clusters/connection-modes.md).

## Generating one

```json title="POST /api/v1/clusters/:id/kubeconfig/generate"
{ "ttl_seconds": 3600, "namespace": "team-a" }
```

Both fields are optional. `ttl_seconds` defaults to 1 hour. `namespace` defaults to the first namespace in your grant, or `default` for a cluster-wide grant. A namespace outside your grant is refused: `403 namespace is outside your granted scope`.

The response carries the file (`kubeconfig`, `filename`), `expires_at`, `k8s_role`, `connection_mode`, `server`, `service_account` (direct mode only) and a `warning` when something needs fixing.

<figure markdown>
  ![The kubeconfig sheet](../assets/screenshots/kubeconfig-sheet.png)
  <figcaption>The generate sheet. The ladder offers only the windows this install's ceiling allows, and the countdown runs against the window the cluster granted.</figcaption>
</figure>

## The TTL ladder and the two ceilings

| Bound | Value | Meaning |
| --- | --- | --- |
| Shortest window | 10 minutes | Floor for a request. |
| Default window | 1 hour | When a request names none. |
| Default ceiling | 24 hours | What an install allows until an administrator says otherwise. |
| Absolute ceiling | 90 days | No setting can exceed it; beyond a quarter a bearer token is a permanent key. |

The ceiling in between is the setting `kubeconfig_max_ttl_hours`, a plain number of **hours** under **Admin → Settings**. A value below an hour, above the absolute ceiling, or `0` reads as unset and falls back to 24 hours.

`GET /api/v1/kubeconfig/policy` returns `min_ttl_seconds`, `default_ttl_seconds` and `max_ttl_seconds` to **any signed-in user**, so the generate sheet can offer only the windows that will be accepted (a fixed ladder from 1h to 90d). Raising the ceiling past a day is disclosed in the Settings warnings and again in the sheet, because the modes differ on revocation (see [below](#revocation-differs-by-mode)).

??? info "Why hours, and why unset on a bad value"
    A ceiling that must move both ways (a quarter for one install, an eight-hour shift for another) cannot be expressed in whole days. A ceiling read wrong is either every request refused or a credential longer than this build will sign, so an ambiguous value takes the safer default.

## What the file contains, by connection mode

### Agent mode

No cluster credential is stored and kubemg cannot dial the cluster, so the file points at kubemg.

- `server` is `{public_url}/api/v1/clusters/:id/proxy`.
- The bearer token is a **kubemg-issued token scoped to that cluster's proxy route**, not a Kubernetes credential. It cannot be used against the rest of the kubemg API.
- `certificate-authority-data` carries the **bastion's own CA** when one is pinned (self-signed, or supplied via `KUBEMG_AGENT_CA_BUNDLE`). A publicly trusted certificate embeds nothing, since pinning it would break the file at renewal.

### Direct mode

- `server` is the cluster's own API URL, with its stored CA.
- The token is minted **on the cluster** via the Kubernetes `TokenRequest` API for a ServiceAccount named after the caller; `service_account` names it.

| Mode | `certificate-authority-data` |
| --- | --- |
| Agent, self-signed or custom CA | The bastion's CA |
| Agent, publicly trusted | Empty |
| Direct | The target cluster's CA |

## Warnings you may see

**Granted shorter than requested (direct mode only).** The cluster's API server may cap token lifetime (`--service-account-max-token-expiration`) and silently issues a shorter one. The response reports what the cluster **granted**, and `warning` says so, e.g. it issued 1 hour instead of the 1 day requested. Raise the API server's own limit, or register the cluster in agent mode.

**Public URL is not HTTPS.** `kubectl` refuses to send a bearer token over plain HTTP. The file still renders but carries a warning to put TLS in front of kubemg. See [TLS](../install/tls.md).

## The register of issued credentials

Every generated kubeconfig is recorded: holder, cluster, mode, namespace, role, who issued it, when, when it expires and when it was last used. The same act is audited as `kubeconfig-issue`, naming both the requester and the holder (so an admin generating a file for someone else is visible). You cannot withdraw what was never recorded, which is why the register exists.

Routes: `GET /api/v1/kubeconfigs`, `POST /api/v1/kubeconfigs/:id/revoke`, `POST /api/v1/kubeconfigs/revoke-all`.

- **Who sees what:** everyone reads; a non-admin sees only their own rows, and `user_id` can narrow but never widen that.
- **Who revokes:** revoking your own is never admin-only (a lost laptop should not need an administrator); somebody else's always is.
- **Where:** **Admin → Identity → Issued credentials** for the fleet, `/me/credentials` for your own. `?expiring=24h` (any window from `15m` to `30d`) narrows to live credentials expiring within it.
- **Last used** is updated at most once every five minutes per credential. A credential generated and never used is the most useful row.
- **Retention** follows the audit window; revoked and expired rows are kept.

## Revocation differs by mode

- **Agent mode:** the token's ID is checked against the register on every proxied call, and the next call after a revoke gets `401`. Other replicas agree within 30 seconds. If the register cannot be read, the previous list is kept: an outage never locks everyone out and never silently honours nothing new.
- **Direct mode:** the token was minted by the cluster, so kubemg cannot withdraw it; it works until it expires. The per-row Revoke is **offered in agent mode only**, and a direct-mode row says why. The one lever is cluster-side: delete the per-user `kubemg-<username>` ServiceAccount, which invalidates all of that user's direct tokens on that cluster. It is not instant, because the API server caches a successful authentication for about ten seconds.

**Disabling the account** and **revoking the grant** both take effect on the very next call and remain the fastest blunt tools. The register adds the case where the laptop was stolen but the person still works here.

### Revoking everything one person holds

`POST /api/v1/kubeconfigs/revoke-all` states what it actually reached:

```json title="200 OK"
{
  "revoked": 3,
  "still_valid": 1,
  "clusters_not_reached": ["prod-eu"],
  "explanation": "These clusters are registered for direct API access, so their credentials are tokens the clusters themselves minted…"
}
```

kubemg never reports a revoke that did not happen. Each withdrawal is its own `kubeconfig-revoke` audit record, and a direct-mode one carries the reason it did not land.

### Rotating a password can take them with it

`POST /api/v1/auth/password` changes your **own** password (current password required) and accepts `revoke_kubeconfigs: true`, which runs the same blanket revoke and returns its summary:

```json title="200 OK"
{ "changed": true, "credentials": { "revoked": 2, "still_valid": 0 } }
```

It is offered, never silent: rotating because of a leak wants the files gone, rotating on a schedule does not. The audit trail records one `password-change` and one `kubeconfig-revoke` per credential.

In the console it is **Change password** on your profile (`/me/profile`) and on `/me/credentials`. It is absent for a federated account (change it with the provider) and for a machine account, whose credential is a [machine token](machine-accounts.md).

See [Adding a cluster](../clusters/registering.md) for registration.
