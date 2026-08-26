# REST API

Every authenticated route lives under `/api/v1`. A handful of routes exist
outside it because they cannot carry a kubemg session — the agent tunnel,
the installer kubectl fetches, and the health check — and are listed
separately at the end.

## Authentication

Two credential shapes arrive at the same middleware (`RequireAuth`):

- **A session JWT**, from `POST /auth/login` or an SSO callback. Sent as
  `Authorization: Bearer <token>`.
- **A machine token**, `kmgm_`-prefixed, issued from the
  [machine accounts](#machine-accounts) surface. Sent the same way, and
  resolved by a stored-hash lookup rather than a JWT parse.

A JWT with `scope: "proxy"` (what a generated kubeconfig carries) is confined
to exactly one route — `/api/v1/clusters/:id/proxy/*path` for the cluster ID
in its claims — and is refused with `403` anywhere else. See
[How a request flows](../introduction/request-flow.md).

`?access_token=<token>` is accepted **only** on a WebSocket upgrade request
(the in-browser terminal), because a browser cannot set headers when opening
a WebSocket. On an ordinary request it is ignored — a token in the URL would
otherwise end up in proxy logs and browser history for no reason, since a
header works there.

## Error shape

Every error response is a flat JSON object:

```json
{"error": "namespace payments is outside your granted scope"}
```

There is one deliberate exception on the proxy route: an error the *target
cluster's* API server returned is forwarded byte for byte, as Kubernetes'
own `Status` object, not reshaped into kubemg's envelope — see
[Troubleshooting](troubleshooting.md#kubectl-through-the-proxy) for how to
tell the two apart.

## Conventions used in the tables below

- **Admin** — requires `RequireRole("admin")` (a super admin qualifies too).
- **Narrows** — a non-admin caller is silently restricted to rows tied to
  their own identity rather than refused outright; the query parameters
  cannot widen it.
- Routes under `/clusters/:id/resources`, `/clusters/:id/metrics` and the
  observability query routes are additionally gated on the cluster's own
  grant (a caller with no access to the cluster gets `403` regardless of
  role), and on agent mode — a direct-mode cluster answers `409` on anything
  that needs a tunnel (Explore, exec, logs, port-forward, CRD discovery,
  session recording).

## Auth

| Method & path | Auth | Notes |
| --- | --- | --- |
| `POST /auth/login` | — | Body `{username, password}`. `200` `{token, expires_at, user}`. `401` on any bad credential (unknown user, wrong password, a federated or machine-account username) — deliberately identical, to prevent username enumeration; a constant-time dummy bcrypt check runs on every failure path. `403` if the account is disabled. |
| `GET /auth/me` | Session | Current `user` object. `401` invalid/expired/deleted account, `403` disabled. |
| `GET /version` | Session | `{version, docs_url}` — the release this process was built as, and the manual for it. Behind a session on purpose: an exact version is what an unauthenticated scanner needs to match a published advisory against the install. A build with no version stamped answers `"unknown"`. |

```bash
curl -sk https://localhost:8443/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}'
```

## Setup

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /setup/state` | — | `{required: bool}`. Unauthenticated by necessity — the sign-in page needs this before anyone has a session. |
| `GET /setup/preflight` | Admin | `{admin_password_pristine, checks[], warnings[]}` — the setup wizard's own checklist. |
| `POST /setup/complete` | Admin | `200 {required:false}` if already done (idempotent). `409` if the bootstrap admin account still holds its seeded password. |

## Federated sign-in (SSO)

All unauthenticated by necessity — nobody has a session yet.

| Method & path | Notes |
| --- | --- |
| `GET /auth/sso/providers` | `{providers:[{id,name,protocol,interactive}]}`. Disabled providers are omitted entirely. |
| `GET /auth/sso/providers/:id/login` | Redirects (`302`) to the IdP. `400` for a non-interactive protocol (LDAP) or a bad `redirect_uri`. |
| `POST /auth/sso/providers/:id/login` | LDAP only — posts `{username,password}` straight to the directory. `401` bad credentials, `502` directory unreachable. |
| `GET`/`POST /auth/sso/providers/:id/callback` | OIDC returns as a `GET` with a code; SAML posts an assertion. On success, `302`-redirects to the console with the token in the URL **fragment** (`#token=...&expires_at=...`), never the query string. |
| `GET /auth/sso/providers/:id/metadata` | SAML only — the SP metadata document, `application/samlmetadata+xml`. |

### SSO administration

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /admin/sso/providers` | Admin | Full config, including `has_client_secret`/`has_bind_password` rather than the secrets themselves, plus computed `redirect_url`/`entity_id`/`metadata_url`. |
| `POST /admin/sso/providers` | Admin | `409` on a name conflict. |
| `PUT /admin/sso/providers/:id` | Admin | `404` if not found. |
| `DELETE /admin/sso/providers/:id` | Admin | `204`. Accounts already provisioned through this IdP are not removed. |
| `POST /admin/sso/providers/:id/check` | Admin | Live probe against the IdP (OIDC discovery, SAML metadata, or an LDAP bind); records health. |
| `GET /admin/sso/mappings` | Admin | IdP-group-to-kubemg-grant rules. |
| `POST /admin/sso/mappings` | Admin | Body: `{provider_id, external_group_pattern, target_group_id?, target_k8s_role?, environment_filter?, namespaces[], target_system_role?}`. `400` if the rule grants nothing, or a namespace/environment filter is given with no `target_k8s_role`. |
| `PUT /admin/sso/mappings/:id` | Admin | |
| `DELETE /admin/sso/mappings/:id` | Admin | |

## Clusters

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /clusters` | Session | Clusters the caller may access; role/namespaces narrowed per grant unless admin. |
| `POST /clusters` | Admin | Body `{name, environment, description?, connection_mode?, api_url, ca_cert_data, service_account_token}` — direct mode requires `api_url`+token; agent mode mints a registration token instead. `409` name conflict. |
| `GET /clusters/:id` | Session | `404` if not found or not granted. Includes `agent_attached` (live tunnel state). |
| `DELETE /clusters/:id` | Admin | `204`. |
| `POST /clusters/:id/check` | Admin | Re-probes health — tunnel connectivity in agent mode, a real dial in direct mode. |
| `POST /clusters/:id/kubeconfig/generate` | Session | Body `{ttl_seconds?, namespace?}`. `400` TTL outside the configured policy. `424` if the server has no token minter/proxy/public URL configured for the mode in play. Response carries `warning` for a shortened direct-mode TTL (the cluster's own SA token cap) or a non-HTTPS public URL. |
| `GET /kubeconfig/policy` | Session | `{min_ttl_seconds, default_ttl_seconds, max_ttl_seconds}` — server-wide, not per cluster. |
| `GET /clusters/:id/kustomize` | Admin | `409` on a direct-mode cluster (nothing to install). `?format=yaml` downloads the flat manifest; otherwise JSON. |

```bash
curl -sk https://localhost:8443/api/v1/clusters \
  -H "Authorization: Bearer $TOKEN"

curl -sk -X POST https://localhost:8443/api/v1/clusters/3/kubeconfig/generate \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"ttl_seconds": 3600}'
```

## Observability sources, metrics and logs history

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /clusters/:id/observability` | Session | `{sources, agent_attached, connection_mode, editable}`. Readable by anyone the cluster is granted to. |
| `PUT /clusters/:id/observability/sources/:kind` | Admin | `kind` is `metrics` or `logs`. `409` registering an in-cluster source on a direct-mode cluster. Probes live on save. |
| `DELETE /clusters/:id/observability/sources/:kind` | Admin | `204`; `404` no such source. |
| `POST /clusters/:id/observability/sources/:kind/test` | Admin | Probes a draft nobody has saved yet — what makes the wizard's "check connection" honest. |
| `POST /clusters/:id/observability/sources/:kind/check` | Admin | Re-probes the stored source and records the verdict. |
| `GET /clusters/:id/observability/discover` | Admin | Reads Services through the tunnel and offers guessed datasource endpoints, with the path prefixes that are the usual reason a correct address 404s. |
| `GET /clusters/:id/observability/metrics/query` | Session | Query params `metric, namespace?, pod?, container?, start?/end?/range?` — never a raw query. `404` `unconfigured` if no datasource, `409` disabled, `400` a cluster-wide chart requested by a scoped grant. |
| `GET /clusters/:id/observability/metrics/compare` | Session | Same as above plus `topk`. |
| `GET /clusters/:id/observability/logs/query` | Session | Adds `filter, limit`; the filter is quoted as a literal so a caller can search for a quote without it becoming query syntax. |

## CRD visibility

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /clusters/:id/crd-visibility` | Session | `{hidden:[...], editable}` — readable as widely as the cluster is granted. |
| `PUT /clusters/:id/crd-visibility` | Admin | Body `{hidden:[]}` of `plural.group` strings, capped at 500 entries, `400` on an invalid entry. Invalidates the cluster's read cache on save. |

## Consoles

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /clusters/:id/consoles` | Session | `{consoles, datasource_uis, editable}`. |
| `PUT /clusters/:id/consoles/:kind` | Admin | `kind` is `grafana`, `argocd` or `registry`. Body `{url, ref?}`; `400` on an invalid URL or one carrying userinfo (a credential in a stored link is refused, not stripped). |
| `DELETE /clusters/:id/consoles/:kind` | Admin | `204`; `404` none registered. |

## Helm repositories

Server-wide, not scoped to a cluster — see [Chart
repositories](../clusters/helm-repositories.md).

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /helm/repositories` | Session | Open to any signed-in user; `has_credential` only, never the credential itself. |
| `GET /helm/repositories/:name/charts` | Session | Query `q?, limit?`. Reads the stored catalogue, not a live fetch. |
| `PUT /helm/repositories/:name` | Admin | Fetches the index synchronously and reports the result, but stores the row even on failure (`status:"error"`). `oci://` and `file://` are each refused with their own message. Omitting `credential` keeps the stored one; `""` clears it. |
| `DELETE /helm/repositories/:name` | Admin | `204`. |
| `POST /helm/repositories/:name/sync` | Admin | Re-runs the fetch on demand, outside the 1-hour schedule. |

## Resources

Every route below is `/api/v1/clusters/:id/resources/...` and answers `409`
on a direct-mode cluster (there is no tunnel to carry the read). Lists share
`listResponse`'s `truncated`/`truncated_at` fields; `all_namespaces=true`
fans a scoped grant out across its own namespaces (capped, `400` past the
fan-out limit) rather than listing the whole cluster.

| Method & path | Notes |
| --- | --- |
| `GET /namespaces` | A scoped grant gets synthetic rows for its own namespaces without touching the cluster; an unscoped grant lists live ones. |
| `GET /workloads` | Deployments, StatefulSets and DaemonSets merged into one list. |
| `GET /pods` | |
| `GET /pods/:pod` | |
| `GET /pods/:pod/logs` | Query `tail` (1–5000, default 200), `container?`, `previous?`. |
| `GET /workload/pods` | Resolves a Deployment/StatefulSet/DaemonSet/ReplicaSet/Job to its pods via a **derived** label selector, never a caller-supplied one — capped at 50 pods. |
| `GET /deployments` `/statefulsets` `/daemonsets` | |
| `GET /jobs` `/cronjobs` | CronJob rows carry `next_schedule_at` or `schedule_error`. |
| `GET /services` `/ingresses` `/httproutes` `/virtualservices` | Gateway API/Istio kinds answer `available:false`+`reason` on a 404 rather than an error. |
| `GET /networkpolicies` `/networkpolicies/reachability` `/networkpolicies/coverage` | Derived reachability and coverage; carries a disclaimer that it does not model the CNI. |
| `GET /persistentvolumes` `/persistentvolumeclaims` `/storageclasses` `/configmaps` | |
| `GET /secrets` | **Keys only. No value ever enters the response.** |
| `GET /crds` | Curated by [CRD visibility](#crd-visibility) for a non-admin. |
| `GET /nodes` | |
| `GET /roles` `/clusterroles` `/rolebindings` `/clusterrolebindings` `/serviceaccounts` | |
| `POST /access-review` | Body `{subject, verb, resource, group?, subresource?, name?, namespace?, groups[]}`. Runs a real `SubjectAccessReview` down the tunnel. `403` if the namespace is outside a scoped grant, or the review itself is cluster-wide for a scoped caller. |
| `GET /access-review/verbs` | Verb catalogue for the form above. |
| `GET /access-review/identity` | `{subject, groups, k8s_role, namespaces, cluster}` — the caller's actual impersonation identity. |
| `GET /custom` | Query `group,version,plural,scope?`. Anchored-pattern validated; the **core group is refused** (must contain a dot). A 404 from the cluster answers `available:false`. |
| `GET /helm/releases` | Deduplicated to the highest revision per release. |
| `POST /helm/releases` | Installs from a registered [chart repository](../clusters/helm-repositories.md), version resolved against the stored catalogue. `409` if a release of that name already exists. Pre-flight refuses a cluster-scoped object or an out-of-grant namespace before the first write. |
| `POST /helm/releases/:name/upgrade` | Re-renders and three-way merges onto the live cluster. Objects the previous revision wrote and this one drops are deleted last, never fatally. |
| `GET /helm/releases/:name/values` | |
| `PUT /helm/releases/:name/values` | Renders and applies, the same as an upgrade, reading the chart back off the release itself — no repository needs to be reachable. `helmValuesWarning` appears only for a release whose stored object carries no chart, naming that reason. |
| `GET /helm/releases/:name/history` | Every stored revision, newest first. |
| `POST /helm/releases/:name/rollback` | Applies the target revision's stored manifest, three-way merged like an upgrade. `404` unknown revision, `409` if it's already current or if the target revision recorded no manifest. |
| `GET /object` | Query `kind,name,namespace?`. |
| `PUT /object` | Body `{yaml}`. `409` for a kind the editor treats as read-only. Conditional on `resourceVersion`. |
| `POST /object` | Creates into the collection derived from the manifest's own `apiVersion`+kind. `409` for `notCreatable` kinds (Roles, RoleBindings, ClusterRoles, ClusterRoleBindings, Nodes). `201` on success. |
| `DELETE /object` | Always `propagationPolicy=Background`. Response says "marked for deletion", not "deleted". No bulk route — a selection is one call per object from the browser. |
| `POST /object/diff` | Pre-image vs. a proposed manifest. |
| `POST /scale` | Body `{kind,name,namespace?,replicas}`. Goes through the `scale` subresource. `409` non-scalable kind, `400` replicas outside `[0,1000]`. |
| `POST /restart` | Stamps `kubectl.kubernetes.io/restartedAt`. `409` a kind with no pod template. |
| `POST /suspend` | CronJob only. A request for the state the object is already in is answered without a write. |
| `GET /describe` | Metadata, `status.conditions`, a bounded flatten of spec/status, and the object's own events (both legacy and `events.k8s.io` shapes). |
| `GET /events` | Filters `range/since/until, kind?, name?, type?`. |
| `GET /posture` | Fixed posture rules per workload; `findings` are never dropped, only acknowledged. |
| `POST` / `DELETE .../posture/ack` | Requires an edit-or-above grant (`403` for `view`). `reason` is mandatory. Audited. |
| `GET /counts?keys=a,b,c` | Batched, read at `limit=1` against `remainingItemCount` so cost is flat regardless of cluster size. `400` past 48 keys, or past 96 effective calls once namespaces multiply in. |

```bash
curl -sk "https://localhost:8443/api/v1/clusters/3/resources/pods?namespace=payments" \
  -H "Authorization: Bearer $TOKEN"
```

## Metrics (live utilisation)

| Method & path | Notes |
| --- | --- |
| `GET /clusters/:id/metrics/nodes` | Cluster-wide only — refused to a namespace-scoped grant. `404`/`503` from metrics-server answers `available:false`, not an error. |
| `GET /clusters/:id/metrics/pods` | Scoped like any other list. |
| `GET /clusters/:id/metrics/pods/:pod` | |
| `GET /clusters/:id/metrics/capacity` | Allocation (requests/limits) against allocatable, not consumption. |

## Machine accounts

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /machine-accounts` | Admin | Each row carries `token_count`, `active_tokens`, `last_used_at?`, and its cluster grants. |
| `POST /machine-accounts` | Admin | Body `{username, email?}`. `409` name taken. Always created `SystemRoleUser`, no password. |
| `PATCH /machine-accounts/:id/status` | Admin | Enable/disable. |
| `DELETE /machine-accounts/:id` | Admin | Deletes its tokens too. |
| `GET /machine-accounts/:id/tokens` | Admin | |
| `POST /machine-accounts/:id/tokens` | Admin | Body `{name, cluster_id, namespace?, ttl_seconds? or never_expires}`. `409` disabled account, direct-mode cluster (refused outright — no revocable credential to hand out), or no grant yet on that cluster. `424` no proxy/public URL configured. Response `{token, secret, kubeconfig, ...}` — **`secret` is shown once**, `kmgm_`-prefixed. |
| `DELETE /machine-accounts/:id/tokens/:tokenId` | Admin | `404` if the token belongs to a different account (never discloses whose). The row is kept, marked revoked. |

## Users

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /users` | Admin | Excludes machine accounts. |
| `POST /users` | Admin | Body `{username, email?, password(min 8), system_role, can_view_recordings?}`. `403` a non-super-admin creating a super admin or granting recording access. `409` username taken. |
| `PUT /users/:id` | Admin | `403` changing your own system role, or a non-super-admin granting super admin/recording access. |
| `PATCH /users/:id/status` | Admin | `403` disabling your own account. |
| `DELETE /users/:id` | Admin | `403` deleting your own account. |

## Groups

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /groups` `POST /groups` | Admin | |
| `DELETE /groups/:id` | Admin | Cascades its grants. |
| `POST /groups/:id/members` | Admin | Body `{user_id}`. `404` unknown group or user. |
| `DELETE /groups/:id/members/:userId` | Admin | `404` if not a member. |

## Permissions

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /permissions` | Admin | `{user_permissions, group_permissions}`, each row carrying `source` and `expires_at` — a live JIT elevation shows up here. |
| `POST /permissions/assign` | Admin | Body `{subject_type, subject_id, cluster_id, k8s_role, namespaces[]}`. Replaces an existing grant for that subject/cluster/source. |
| `POST /permissions/revoke` | Admin | Body `{subject_type, subject_id, cluster_id}`. `404` no such grant. |

## Machine-readable settings

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /settings` | Admin | `{effective, overrides, defaults, warnings}` — three-tier resolution. |
| `PUT /settings` | Admin | An empty string or `0` clears an override back to the default. `public_url`, `agent_namespace`, and the TTL/retention fields are each validated on the way in. |
| `GET /settings/deployment` | Admin | `{checks[], attention}` — deployment posture; see [Troubleshooting](troubleshooting.md). |

See [Runtime settings](settings.md) for what each field means.

## Guardrails

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /guardrails` | Admin | `?cluster_id=` filters (`0` is a real fleet-wide filter, distinct from omitting it). Response includes `enforcing`, the live compiled rule count — which can differ from the stored count if a pattern fails to compile. |
| `GET /guardrails/templates` | Admin | Preset catalogue. |
| `POST /guardrails` | Admin | Body `{name, description?, cluster_id?, pattern, target?, action?, enabled?}`. `400` an invalid pattern or an unknown `cluster_id`. |
| `PUT /guardrails/:id` `DELETE /guardrails/:id` | Admin | |

## Alarms

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /alarms/channels` `POST /alarms/channels` | Admin | A channel's secret is write-only — never read back. |
| `PUT /alarms/channels/:id` `DELETE /alarms/channels/:id` | Admin | `409` duplicate name. `400` invalid header JSON, or a missing PagerDuty routing key/basic-auth username. |
| `POST /alarms/channels/:id/test` | Admin | Bypasses the matcher and cool-off. `200` even if delivery itself failed — the test is about the endpoint, not the rule. |
| `GET /alarms/rules` `POST /alarms/rules` | Admin | Response includes the trigger/severity vocabulary alongside the rules. |
| `PUT /alarms/rules/:id` `DELETE /alarms/rules/:id` | Admin | |

## Just-in-time access

| Method & path | Auth | Notes |
| --- | --- | --- |
| `POST /jit/requests` | Session | Body `{cluster_id, requested_role, namespaces[], duration_minutes, reason}`. `reason` is mandatory. |
| `GET /jit/requests` | Session, narrows | Non-admin forced to their own requests. |
| `POST /jit/requests/:id/approve` | Admin | Never the requester's own — enforced even for a super admin. |
| `POST /jit/requests/:id/reject` | Session | Not admin-only: withdrawing your own request needs no permission. |
| `POST /jit/requests/:id/revoke` | Session | Handing an elevation back early needs no permission either. |
| `POST /jit/webhooks/callback` | Unauthenticated | Requires a valid Slack signature, a signed non-expired action token, **and** an `approver_username` resolving to an active, non-requester admin — the token alone is not enough. |

## Audit and terminal sessions

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /audit` | Session, narrows | Filters `verb[], status, from/to, since/until,` or a fixed range preset. Non-admin forced to their own `user_id`; the query cannot widen it. |
| `GET /audit/summary` | Admin | 24-hour rollup: `{total, failed, streams, window_hours}`. |
| `GET /audit/recording-policy` | Session | `{enabled, input_recorded, encrypted, retention_days}` — disclosed to anyone, since anyone might be recorded. |
| `GET /audit/terminal-sessions` | Session, narrows | `recording_enabled: false` is distinct from a genuinely empty list. |
| `GET /audit/terminal-sessions/:id` | Session, narrows | `404`, not `403`, for someone else's session without the capability — whether it exists is not theirs to learn. |
| `GET /audit/terminal-sessions/:id/stream` | Session, narrows | `503` recording disabled, `404` file missing, `409` for a key-required/key-mismatch/truncated recording (three distinct messages). Audited as `replay` before any bytes are sent. |
| `DELETE /audit/terminal-sessions/:id` | Admin + `CanViewRecordings` | Audited before the file is removed. |

## Non-API routes

| Method & path | Auth | Notes |
| --- | --- | --- |
| `GET /health` | — | `{"status":"ok"}`. Process liveness only — nothing about TLS, the database or the tunnel. |
| `GET /agent/v1/tunnel` | Agent registration token | The tunnel's WebSocket upgrade. Outside the JWT middleware entirely — an agent authenticates on its own registration token as a bearer token on the upgrade. |
| `GET /install/:token/agent.yaml` | Registration token in the path | Unauthenticated by necessity — `kubectl` cannot carry a kubemg session; the token in the URL *is* the credential. Renders the flat install manifest. |
| `GET /install/:token/kustomize.tar.gz` | Registration token in the path | Same route family, the Kustomize archive instead. |
| `ANY /api/v1/clusters/:id/proxy/*path` | Session or `ScopeProxy` JWT or machine token | The `kubectl` server URL. Every verb — get, watch, exec, port-forward — lands on this one route; see [How a request flows](../introduction/request-flow.md). |

```bash
# Read the audit trail, narrowed to your own activity unless you're an admin
curl -sk "https://localhost:8443/api/v1/audit?range=1h" \
  -H "Authorization: Bearer $TOKEN"
```
