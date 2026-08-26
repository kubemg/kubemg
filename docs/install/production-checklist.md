# Production checklist

Expanded from the README's own checklist — each item says why, and where to
read the full detail.

- [ ] **Real TLS material in place**, either dropped into `/etc/kubemg/ssl`
      (`ssl/` beside the compose file, or a mounted Secret in Kubernetes) or
      `KUBEMG_AGENT_CA_BUNDLE` set behind an ingress that terminates TLS
      itself. Without real TLS, every browser and every agent trusts a
      self-signed certificate you'll need to re-pin fleet-wide the moment you
      replace it. **Settings → Deployment** in the console reports which
      certificate is actually in force at any time. See
      [TLS and certificates](tls.md).

- [ ] **Bootstrap admin password changed.** Setup refuses to finish while the
      account created on first boot still holds the password that was
      generated and logged — so getting through the setup wizard once is
      what ticks this box; there's no separate step. See
      [Environment reference](environment.md#auth-jwt-bootstrap).

- [ ] **`JWT_SECRET` set explicitly** if you want a deliberate, known
      signing key you control the rotation of — otherwise the server mints
      one on first boot and keeps it in the database (safe across several
      replicas booting at once; the write is a conflict-safe upsert). Setting
      it explicitly is what gives you a way to invalidate every issued
      session and kubeconfig at once, by rotating it yourself. See
      [Choosing a deployment](index.md#sizing-and-high-availability).

- [ ] **`KUBEMG_SESSION_RECORDING_KEY` generated per install**, and kept
      *out of* whatever backs up the recordings volume. Every interactive
      `exec`/`attach` session is recorded for replay, and what a recording
      holds is everything a shell saw — up to and including credentials that
      never should have been typed there in the first place. Storing the key
      alongside the ciphertext it protects defends against nothing. See
      [Environment reference](environment.md#session-recording).

- [ ] **`KUBEMG_SESSION_RECORDING_DIR` on a persistent volume.** An
      unmounted recordings directory means every replay vanishes on the next
      restart — which is the audit evidence an incident review or an
      auditor will ask for. See
      [Choosing a deployment](index.md#what-the-management-plane-needs-regardless-of-where-it-runs).

- [ ] **`KUBEMG_PUBLIC_URL` set to the address your clusters actually
      dial, over HTTPS.** This is baked into every generated agent install
      command and every issued kubeconfig; `localhost` or the container's own
      address produces an agent that dials itself and never connects. See
      [Environment reference](environment.md#public-url-agent).

- [ ] **Managed PostgreSQL with `DB_SSLMODE=require`** (or stricter). The
      default (`disable`) is a development convenience — every user, grant,
      cluster and audit row is in this database, so its own transport should
      not be plaintext on any network you don't fully trust. See
      [Database](database.md).

- [ ] **Retention window set to whatever your auditors need**
      (`KUBEMG_AUDIT_RETENTION_DAYS`, or the equivalent Settings page field
      at runtime) — the audit trail and, by default, session recordings are
      pruned on this window in the background. A window shorter than your
      compliance requirement quietly deletes evidence before anyone asks for
      it. See [Environment reference](environment.md#audit-retention).

- [ ] **The TLS certificate volume backed up separately from the
      database.** Losing `tls-certs` mints a fresh self-signed certificate
      on the next boot, and every already-installed agent then fails its
      handshake against a certificate it doesn't recognize — this is a
      fleet-wide incident, not a config fix. See
      [Docker Compose](docker-compose.md#the-volumes-and-which-to-back-up) or
      [Kubernetes](kubernetes.md#namespace-secret-pvcs).

- [ ] **Agent manifests are current on every attached cluster.** Agent RBAC
      has gained permissions between releases without a protocol bump (CRD
      discovery and custom-resource read/write); an agent that hasn't
      re-applied answers CRD discovery with a silent `403` and an empty
      Explore sidebar for custom resources. See
      [Upgrading](upgrading.md#when-agents-must-re-apply-their-manifests).

- [ ] **You understand which connection mode each cluster uses, and what
      that means for RBAC.** In direct mode, kubemg mints tokens but
      provisions no RoleBinding on the target cluster — a generated
      kubeconfig authenticates without authorizing, and the permission
      matrix governs kubemg's own authorization only. Agent mode is where
      the cluster's own RBAC decides. This is a deliberate, disclosed
      limitation, not a bug to work around — the cluster detail page, the
      permissions page and the registration wizard all say which applies.
      See [Connection modes](../clusters/connection-modes.md).

- [ ] **`CORS_ALLOWED_ORIGINS` is unset (or irrelevant)** in a real
      deployment. If you're seeing a CORS error in production, it's almost
      always a sign the console is being served from somewhere other than
      the same origin as the API — which the single-image deployment
      (Compose or Kubernetes) doesn't need to do at all. See
      [Environment reference](environment.md#cors).

## Next

- [TLS and certificates](tls.md)
- [Environment reference](environment.md)
- [Upgrading](upgrading.md)
