# Production checklist

Check these before real users or clusters depend on the install. Each item says
why and links to the detail.

- [ ] **Real TLS in place**: material in `/etc/kubemg/ssl` (`ssl/` beside the
      compose file, or a mounted Secret), or `KUBEMG_AGENT_CA_BUNDLE` set
      behind an ingress that terminates TLS. Replacing a self-signed
      certificate later means re-pinning the whole fleet. **Settings →
      Deployment** reports the certificate in force. See
      [TLS and certificates](tls.md).

- [ ] **Bootstrap admin password changed.** Setup will not finish while the
      generated password is unchanged, so completing setup ticks this box. See
      [Environment reference](environment.md#auth-jwt-bootstrap).

- [ ] **Exactly one kubemg replica** (on Kubernetes, `replicas: 1` with
      `strategy: Recreate`). A second replica answers `503` for clusters whose
      agent connected to the other. See
      [Choosing a deployment](index.md#sizing-and-high-availability).

- [ ] **`JWT_SECRET` set explicitly**, if you want a known signing key.
      Otherwise the server mints one and keeps it in the database. Rotating
      your own key invalidates every issued session and kubeconfig at once. See
      [Choosing a deployment](index.md#sizing-and-high-availability).

- [ ] **`KUBEMG_SECRET_KEY` generated per install**, backed up apart from the
      database. Without it a database copy holds the session signing key and
      every agent's tunnel credential in the clear. With it, a restored
      database will not boot without the key. Set `JWT_SECRET` too so the
      signing key is not stored in the database at all. See
      [Database](database.md#credentials-encrypted-at-rest).

- [ ] **`KUBEMG_SESSION_RECORDING_KEY` generated per install**, kept *out of*
      the recordings volume's backup. Recordings hold everything a shell saw,
      including credentials typed by mistake. See
      [Environment reference](environment.md#session-recording).

- [ ] **`KUBEMG_SESSION_RECORDING_DIR` on a persistent volume**, or every replay
      vanishes on restart. See
      [Choosing a deployment](index.md#what-the-management-plane-needs-regardless-of-where-it-runs).

- [ ] **`KUBEMG_PUBLIC_URL` is the HTTPS address your clusters dial.** It is
      baked into agent commands and kubeconfigs; `localhost` makes an agent that
      dials itself. See
      [Environment reference](environment.md#public-url-agent).

- [ ] **Managed PostgreSQL with `DB_SSLMODE=require`** or stricter. The default
      (`disable`) is for development, and every user, grant and audit row is in
      this database. See [Database](database.md).

- [ ] **Retention matches your auditors**
      (`KUBEMG_AUDIT_RETENTION_DAYS` or the Settings field). The trail and, by
      default, recordings are pruned on this window; too short quietly deletes
      evidence. See [Environment reference](environment.md#audit-retention).

- [ ] **Database backed up, `KUBEMG_SECRET_KEY` kept apart.** The minted
      certificate lives there and every installed agent pinned it; restoring
      without it means a fleet-wide re-install. See
      [TLS](tls.md#the-minted-certificate-is-kept-in-the-database-too) and
      [Database](database.md#backup-and-restore).

- [ ] **Agent manifests current on every cluster.** Agent RBAC has gained
      permissions between releases; a stale agent answers CRD discovery with a
      silent `403` and an empty custom-resource sidebar. See
      [Upgrading](upgrading.md#when-agents-must-re-apply-their-manifests).

- [ ] **You know each cluster's connection mode.** In direct mode kubemg mints
      tokens but creates no RoleBinding, so a kubeconfig authenticates without
      authorizing and the permission matrix governs only kubemg's own checks.
      In agent mode the cluster's own RBAC decides. This is a disclosed
      limitation. See [Connection modes](../clusters/connection-modes.md).

- [ ] **`CORS_ALLOWED_ORIGINS` unset.** A CORS error in production means the
      console is served from a different origin than the API, which the
      single-image deployment never does. See
      [Environment reference](environment.md#cors).

## Next

- [TLS and certificates](tls.md)
- [Environment reference](environment.md)
- [Upgrading](upgrading.md)
