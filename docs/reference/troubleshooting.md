# Troubleshooting

Symptom → cause → fix, grouped by where the problem sits. Start with
`GET /health`, then `GET /api/v1/settings/deployment` (admin-only,
[deployment posture](#where-to-look)) before anything else — both answer in
under a second and rule out half of this page.

## Install and boot

**Server exits immediately on a fresh install, with no obvious error.**
Check the container logs for `database connection failed` or `database
migration failed` — the process refuses to serve
anything without a reachable, migratable Postgres. Confirm `DB_HOST`/
`DB_PORT`/`DB_USER`/`DB_PASSWORD`/`DB_NAME` and that Postgres is actually
up before the backend container starts (a compose `depends_on` without a
health check races this).

**No `JWT_SECRET` was set, and I'm worried sessions won't survive a
restart.** They will. With `JWT_SECRET` unset, the server generates a key
once and stores it in the database; it is
read back on every subsequent boot, so it does not change under you. It
only changes if the database is wiped, which invalidates every session and
every previously generated kubeconfig at once. Set `JWT_SECRET` explicitly
if you rotate secrets through an external manager.

**Server refuses to start: `refusing to serve plaintext HTTP on
<addr>: it is reachable from more than this machine…`.** By design — a
non-loopback `KUBEMG_LISTEN_ADDR` with TLS off would put every session JWT
on the wire in the clear. Fix it one of three ways: set
`KUBEMG_TLS_ENABLED=true`, bind to loopback (`127.0.0.1:8080`) behind a TLS-
terminating reverse proxy, or set `KUBEMG_ALLOW_INSECURE=true` to start
anyway (development only).

**Server refuses to start: `found only one of <cert> and <key>; supply
both or neither`.** A `KUBEMG_TLS_SUPPLIED_DIR` (or `KUBEMG_TLS_CERT_FILE`/
`KUBEMG_TLS_KEY_FILE`) directory has a `tls.crt` with no matching `tls.key`
(or vice versa). The check is deliberate: generating the missing half
against a key nobody expects would be worse than refusing. Supply both
files, or remove the partial one and let self-signing take over.

**Console loads but every generated kubeconfig / `kubectl exec` fails
immediately.** The server is serving plain HTTP. `client-go` refuses to
send a bearer token over `http://` at all — this is a `client-go`
behavior, not a kubemg bug. Check `GET /api/v1/settings/deployment`: a
`"tls"` check with severity `blocked` and the detail "kubectl cannot use
this bastion over plain http…" confirms it. Set `KUBEMG_TLS_ENABLED=true`.

## The agent not attaching

**Agent logs `"agent handshake failed"` and the tunnel never comes up.**
Three causes, in order of likelihood:

1. **x509: certificate signed by unknown authority.** The bastion is
   self-signed or behind an internal CA the agent does not trust. If the
   agent was installed from the wizard's package, `KUBEMG_BASTION_CA`
   should already carry the pinned certificate — check the agent's Secret.
   If the bastion's certificate was replaced *after* the agent was
   installed, re-download and re-apply the install package (the CA is
   pinned at install time, not fetched live). As a last resort for testing
   only, `KUBEMG_BASTION_INSECURE_SKIP_VERIFY=true` skips verification and
   logs a warning.
2. **Protocol version mismatch.** The bastion logs the same
   `"agent handshake failed"` when an old agent's `hello` names an
   unsupported `ProtocolVersion`. Rebuild/redeploy the agent image the
   current server ships (`KUBEMG_AGENT_IMAGE` in Settings, or the version
   in the freshly downloaded install package).
3. **A bad or reused registration token.** The token is bound to one
   cluster at issuance; a token copied from a different cluster's install
   command, or one already consumed and reissued, is refused at the
   handshake. Re-run the registration wizard for a fresh token rather than
   reusing an old manifest.

**Agent's own `/readyz` reports 503 `tunnel is not connected` even though
`/healthz` is fine.** The process is up but cannot reach
`KUBEMG_BASTION_URL` — check egress (a corporate proxy or an egress
firewall rule blocking outbound WebSocket), that `KUBEMG_BASTION_URL` is
resolvable and reachable *from inside the cluster* (not just from your
laptop), and that the bastion's own address hasn't changed since the agent
was installed (`KUBEMG_PUBLIC_URL` on the server side).

**Cluster shows "registered" in kubemg but never goes live.** Confirm the
agent Deployment is actually running (`kubectl get pods -n
<KUBEMG_AGENT_NAMESPACE>`) and check its logs directly — a CrashLoopBackOff
there never reaches kubemg's own UI at all.

## `kubectl` through the proxy

**`kubectl` fails with a TLS or "bearer token over http" style error, but
the console works fine.** The console doesn't send a bearer token over
plain HTTP the way `client-go` refuses to; this is exactly the TLS-off
case above. Enable TLS on the bastion.

**`kubectl port-forward` fails with HTTP `501` naming
`KUBECTL_PORT_FORWARD_WEBSOCKETS=true`.** Your `kubectl` (pre-1.31, or a
client with the flag disabled) is negotiating the older SPDY transport,
which the proxy does not carry — only the WebSocket shape
(`v2.portforward.k8s.io`) is bridged. Set the environment variable named in
the error, or upgrade `kubectl` to 1.31+, where it is the default.

**A call is refused with `403`, but which side refused it?** Read the
message shape, not just the status code:

- **kubemg's own refusal** is one of a small set of fixed English
  sentences: `"namespace <ns> is outside your granted scope"` (a
  namespace-scoped grant reaching past its namespaces), `"no access to
  this cluster"`, `"this token may only be used against its cluster's
  kubectl proxy"`, or a guardrail's own message. These never reached the
  cluster's API server at all — the call is refused before the tunnel is
  even looked up.
- **The cluster's own RBAC refusal** is handed back **verbatim** from the
  API server's own response, in Kubernetes' own words — something in the
  shape `"pods is forbidden: User \"kubemg:dev@corp\" cannot list resource
  \"pods\" in API group \"\" in the namespace \"payments\""`. This means
  the call reached the agent and the API server evaluated it: the grant in
  kubemg is fine, but the `kubemg:view`/`kubemg:edit`/
  `kubemg:cluster-admin` ClusterRoleBinding does not cover what was asked.
  Check the agent's RBAC manifests (`rbac.yaml`) were applied and are
  current for this fix, especially after an upgrade — see below.

## The console

**Requests fail with a CORS error in the browser console, but `curl`
against the same endpoint works.** `CORS_ALLOWED_ORIGINS` does not include
the origin the browser is actually loading from, or a reverse proxy in
front of kubemg is stripping the `Authorization` or `Cache-Control`
response/request headers. A stock CORS configuration does **not** allow the
`Authorization` header by itself — kubemg's own CORS middleware adds it, along with `Cache-Control` and `Pragma` (the
headers the console's read cache uses); a proxy or CDN in front that
strips or rewrites headers can reintroduce the same symptom kubemg's own
config already avoids.

**The Explore sidebar shows no custom resources (no Istio, no Strimzi,
etc.) after upgrading the agent.** The built-in `kubemg:view`/`kubemg:edit`
roles gained `kubemg-crd-discovery` (read on `customresourcedefinitions`)
and `kubemg-custom-resource-view`/`-edit` (read/write on the specific CRD
groups Explore surfaces) after an earlier release; an agent installed
before that release is still running the old RBAC. Discovery calls answer
`403`, and the sidebar simply shows no custom-resource sections rather than
erroring loudly. Re-download the install package for the cluster
(`GET /api/v1/clusters/:id/kustomize` or the wizard) and re-apply it — the
manifests are idempotent.

**A list says `"truncated": true`, or the UI shows a truncation notice.**
Expected on a very large cluster, not a bug: every list read is paged and
bounded — a page tops out at 250 items (`listPageSize`), and a whole read
across every page (and, for a scoped grant, every namespace) tops out at
2000 items (`maxListItems`) — because the agent itself refuses to tunnel
back more than 8&nbsp;MB in one response. Narrow the namespace or filter
rather than expecting the full list; the count columns elsewhere (sidebar
counts, `remainingItemCount`) are unaffected because they are read at
`limit=1` and cost nothing proportional to cluster size.

## Recordings

**A cluster/session has no recording, and the list is simply empty.**
Check `GET /api/v1/audit/recording-policy` — `recording_enabled: false`
means either nobody opened a shell *or* nobody was recording when they did,
which reads identically as an empty list otherwise; the policy response
disentangles the two. If it's `false` unexpectedly, check
`KUBEMG_SESSION_RECORDING_ENABLED` and `record_exec_sessions` in Settings —
turning the setting off only stops the *next* shell; sessions already open
keep recording until they close.

**Replay fails with a decryption error.** Three distinct causes map to
three distinct fixes, and the server tells you which:
`ErrKeyRequired` ("recording is encrypted and no recording key is
configured") means `KUBEMG_SESSION_RECORDING_KEY` was unset when this
recording was made encrypted but is unset now — restore the key.
`ErrKeyMismatch` ("recording could not be decrypted with the configured
key") means the wrong key is configured now — the evidence still exists,
only the key needs restoring, so don't delete the file. `ErrTruncated`
means the file itself is incomplete or was altered — the session ended
without a final authenticated chunk (a crash, a disk that filled mid-write,
or genuine tampering), and there is nothing to recover from that copy.

## Datasources

**Saving or testing a datasource reports "answered, but not on `<path>`
(HTTP 404)".** This is the single most common misconfiguration: the
address is reachable, but the path prefix is wrong for the backend behind
it — a vmselect endpoint typically needs `/select/0/prometheus`, Grafana
Mimir needs `/prometheus`. Use `GET .../observability/discover` to see what
kubemg found running in the cluster and the prefix it suggests, rather than
guessing.

**Registering an in-cluster datasource on a direct-mode cluster fails with
`409`.** An in-cluster source is reached by asking the target API server to
proxy to a Service, which only exists down the agent tunnel — a direct-mode
cluster has no tunnel. Register it as a `direct` (externally reachable)
source instead, giving its address the way any browser would reach it.

## Where to look

- **`GET /health`** — process liveness, unauthenticated.
- **`GET /api/v1/settings/deployment`** (admin) — the same boot-time
  checks the setup wizard showed, still queryable afterward: TLS state,
  whether the certificate is self-signed and whether it was supplied,
  recording encryption, and admin bootstrap state. This is the single
  fastest way to confirm what the server actually booted with versus what
  you meant to configure.
- **The server's own logs** (structured, `slog`) — boot-time `Warn`/`Fatal`
  lines name the fix directly, as quoted above.
- **The audit trail** (`GET /api/v1/audit`) — every proxied call, refusals
  included; a `403` here disambiguates kubemg's own refusal from the
  cluster's per the rule above, since kubemg's own refusals never reach the
  agent and so are recorded with kubemg's own message.
- **The agent's own logs and `/readyz`** — for anything that looks like
  "the tunnel never came up" rather than "a call through it failed".
