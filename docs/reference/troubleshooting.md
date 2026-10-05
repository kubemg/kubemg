# Troubleshooting

Symptom, cause, fix, grouped by where the problem sits. Find your symptom and
follow the fix. Start with `GET /health`, then `GET /api/v1/settings/deployment`
(admin-only, see [Where to look](#where-to-look)); both answer in under a second
and rule out half of this page.

## Install and boot

**Server exits at once on a fresh install.**

- Cause: Postgres is unreachable or the schema could not be applied. The logs say
  `database connection failed` or `database migration failed`.
- Fix: check `DB_HOST`/`DB_PORT`/`DB_USER`/`DB_PASSWORD`/`DB_NAME`, and that
  Postgres is up before the backend starts (a compose `depends_on` without a
  health check races).

**`JWT_SECRET` is unset; will sessions survive a restart?**

- Yes. The server generates a key once, stores it in the database and reads it
  back on every boot. It changes only if the database is wiped, which
  invalidates every session and generated kubeconfig.
- Set `JWT_SECRET` explicitly if you rotate secrets through an external manager.

**Refuses to start: `KUBEMG_SECRET_KEY is unusable`.**

- Cause: the key is not 32 bytes written as hex or base64.
- Fix: `openssl rand -base64 32`. A passphrase is refused on purpose.

**Refuses to start: `refusing to start: stored credentials cannot be decrypted…`.**

- Cause: the database holds credentials encrypted under a `KUBEMG_SECRET_KEY`
  this server does not have (changed, removed, or restored without it). The
  message names the first unreadable value.
- Fix: restore the original key. If it is truly lost, see
  [Database](../install/database.md#credentials-encrypted-at-rest).

**Refuses to start: `refusing to serve plaintext HTTP on <addr>: it is reachable from more than this machine…`.**

- Cause: a non-loopback `KUBEMG_LISTEN_ADDR` with TLS off would put every session
  JWT on the wire in the clear. This is by design.
- Fix, one of: set `KUBEMG_TLS_ENABLED=true`; bind to loopback
  (`127.0.0.1:8080`) behind a TLS-terminating proxy; or set
  `KUBEMG_ALLOW_INSECURE=true` (development only).

**Refuses to start: `found only one of <cert> and <key>; supply both or neither`.**

- Cause: `KUBEMG_TLS_SUPPLIED_DIR` (or the cert/key file variables) has a
  `tls.crt` without `tls.key`, or the reverse. Generating the missing half is
  refused on purpose.
- Fix: supply both files, or remove the partial one and let self-signing take over.

**Console loads, but every generated kubeconfig or `kubectl exec` fails at once.**

- Cause: the server is serving plain HTTP, and `kubectl` refuses to send a bearer
  token over `http://`. This is `kubectl`'s behaviour, not a kubemg bug.
- Confirm: `GET /api/v1/settings/deployment` shows a `tls` check of severity
  `blocked`, "kubectl cannot use this bastion over plain http…".
- Fix: set `KUBEMG_TLS_ENABLED=true`.

## The agent not attaching

**Agent logs `"agent handshake failed"` and the tunnel never comes up.** Three
causes, most likely first:

1. **`x509: certificate signed by unknown authority`.** The bastion is
   self-signed or behind an internal CA the agent does not trust.
    - If installed from the wizard's package, `KUBEMG_BASTION_CA` should carry
      the pinned certificate; check the agent's Secret.
    - If the bastion's certificate changed *after* install, re-download and
      re-apply the install package (the CA is pinned at install time).
    - For testing only, `KUBEMG_BASTION_INSECURE_SKIP_VERIFY=true` skips
      verification and logs a warning.
2. **Protocol version mismatch.** An old agent names an unsupported protocol
   version, and the bastion logs the same message. Redeploy the agent image the
   current server ships (`KUBEMG_AGENT_IMAGE` in Settings, or the version in a
   freshly downloaded package).
3. **A bad or reused registration token.** A token is bound to one cluster; one
   copied from another cluster's command, or already consumed and reissued, is
   refused. Re-run the registration wizard for a fresh token.

**Agent's `/readyz` reports 503 `tunnel is not connected` while `/healthz` is fine.**

- Cause: the process is up but cannot reach `KUBEMG_BASTION_URL`.
- Fix: check egress (a corporate proxy or firewall blocking outbound WebSocket),
  that `KUBEMG_BASTION_URL` resolves *from inside the cluster*, and that the
  bastion's address has not changed since install (`KUBEMG_PUBLIC_URL`).

**Cluster shows "registered" in kubemg but never goes live.**

- Cause: often the agent never started. A CrashLoopBackOff never reaches the UI.
- Fix: `kubectl get pods -n <KUBEMG_AGENT_NAMESPACE>` and read the agent's logs.

## `kubectl` through the proxy

**`kubectl` fails with a TLS or "bearer token over http" error, but the console works.**
This is the TLS-off case above. Enable TLS on the bastion.

**`kubectl port-forward` fails with HTTP `501` naming `KUBECTL_PORT_FORWARD_WEBSOCKETS=true`.**

- Cause: your `kubectl` (before 1.31, or with the flag off) uses the older SPDY
  transport, which the proxy does not carry. Only the WebSocket transport is bridged.
- Fix: set the variable named in the error, or use `kubectl` 1.31+, where it is
  the default.

**A call is refused with `403`. Which side refused it?** Read the message, not
just the status:

| Message shape | Who refused | Meaning |
| --- | --- | --- |
| A fixed sentence: `namespace <ns> is outside your granted scope`, `no access to this cluster`, `this token may only be used against its cluster's kubectl proxy`, or a guardrail's message | kubemg | The call never reached the cluster. |
| Kubernetes' own words, e.g. `pods is forbidden: User "kubemg:dev@corp" cannot list resource "pods"…` | The cluster's RBAC | The call reached the API server. The kubemg grant is fine but the `kubemg:view`/`edit`/`cluster-admin` bindings do not cover it. Check the agent's RBAC manifests were applied and are current, especially after an upgrade. |

## The console

**Requests fail with a CORS error in the browser, but `curl` works.**

- Cause: `CORS_ALLOWED_ORIGINS` omits the origin the browser loads from, or a
  proxy or CDN in front strips the `Authorization` or `Cache-Control` headers.
  kubemg's own CORS config allows `Authorization`, `Cache-Control` and `Pragma`;
  an intermediary that rewrites headers brings the symptom back.
- Fix: add the origin, and stop the proxy rewriting those headers.

**The Explore sidebar shows no custom resources (Istio, Strimzi, …) after upgrading the agent.**

- Cause: the agent was installed before the release that added the RBAC for
  discovering and reading custom resources. Discovery answers `403` and the
  sidebar silently shows no custom-resource sections.
- Fix: re-download the cluster's install package (`GET /api/v1/clusters/:id/kustomize`
  or the wizard) and re-apply it. The manifests are idempotent.

**A list says `"truncated": true`, or the UI shows a truncation notice.**

- Cause: expected on a very large cluster, not a bug. A page tops out at 250
  items and a whole read (across pages and, for a scoped grant, namespaces) at
  2000, because the agent will not tunnel back more than 8 MB in one response.
- Fix: narrow the namespace or filter. Count columns are unaffected.

## Recordings

**A session has no recording and the list is empty.**

- Cause: `GET /api/v1/audit/recording-policy` shows `recording_enabled: false`.
  An empty list otherwise looks the same whether nobody opened a shell or nobody
  was recording.
- Fix, if unexpected: check `KUBEMG_SESSION_RECORDING_ENABLED` and
  `record_exec_sessions` in Settings. Turning the setting off only stops the
  *next* shell; open sessions keep recording until they close.

**Replay fails with a decryption error.** The server says which of three causes:

| Message | Cause | Fix |
| --- | --- | --- |
| "recording is encrypted and no recording key is configured" | `KUBEMG_SESSION_RECORDING_KEY` is unset now but was set when this was recorded | Restore the key |
| "recording could not be decrypted with the configured key" | Wrong key configured | Restore the right key; the evidence still exists, so do not delete the file |
| Truncated | The file is incomplete or altered: a crash, a full disk mid-write, or tampering | Nothing to recover from that copy |

## Datasources

**Saving or testing a datasource reports "answered, but not on `<path>` (HTTP 404)".**

- Cause: the most common misconfiguration. The address answers, but the path
  prefix is wrong for the backend (a vmselect endpoint typically needs
  `/select/0/prometheus`, Grafana Mimir needs `/prometheus`).
- Fix: use `GET .../observability/discover` to see what kubemg found and the
  prefix it suggests.

**Registering an in-cluster datasource on a direct-mode cluster fails with `409`.**

- Cause: an in-cluster source is reached through the agent tunnel, which a
  direct-mode cluster does not have.
- Fix: register it as a `direct` (externally reachable) source instead.

## Where to look

- **`GET /health`**: process liveness, unauthenticated.
- **`GET /api/v1/settings/deployment`** (admin): the setup wizard's boot-time
  checks, still queryable. TLS state, certificate origin, recording encryption
  and admin bootstrap state: the fastest way to see what the server actually
  booted with.
- **The server's logs**: boot-time warnings and fatal lines name the fix, as
  quoted above.
- **The audit trail** (`GET /api/v1/audit`): every proxied call, refusals
  included. kubemg's own refusals never reach the agent and are recorded with
  kubemg's own message, which tells the two `403`s apart.
- **The agent's logs and `/readyz`**: for "the tunnel never came up" rather than
  "a call through it failed".
