# TLS and certificates

This page explains how kubemg serves HTTPS, which certificate files it accepts, how agents decide what to trust, and how to check it all. Read it before you put kubemg on a real hostname.

TLS is required, not optional: `kubectl` and every generated kubeconfig refuse to send a bearer token over plain `http://`. Without TLS, `kubectl exec`, `kubectl proxy` and every kubeconfig fail, and no server setting fixes that.

## Why the server refuses to start without it

- `KUBEMG_TLS_ENABLED=true`: the server serves HTTPS on `KUBEMG_LISTEN_ADDR`.
- TLS off and a listen address that is **not** loopback (empty host binds every interface): the server refuses to start:

  ```
  refusing to serve plaintext HTTP on :8080: it is reachable from more than
  this machine, and every session JWT would transit unencrypted. Set
  KUBEMG_TLS_ENABLED=true, bind KUBEMG_LISTEN_ADDR to loopback (e.g.
  127.0.0.1:8080), or set KUBEMG_ALLOW_INSECURE=true to start anyway
  ```

- TLS off on a **loopback** bind: allowed with a warning. `kubectl` still will not work against it.
- `KUBEMG_ALLOW_INSECURE=true` overrides the refusal. Use it only when a reverse proxy or ingress in front of kubemg terminates TLS; anywhere else, session tokens and proxied calls travel unencrypted.

## The three ways a certificate ends up in force

kubemg picks what to serve at every boot, in this order:

1. **A supplied certificate wins**, from `KUBEMG_TLS_SUPPLIED_DIR` (default `/etc/kubemg/ssl`).
2. Otherwise, with `KUBEMG_TLS_SELF_SIGNED=true` (the default), a self-signed pair is minted at `KUBEMG_TLS_CERT_FILE`/`KUBEMG_TLS_KEY_FILE` (default `/etc/kubemg/tls/tls.{crt,key}`), **once**. An existing pair there is never overwritten.
3. With `KUBEMG_TLS_SELF_SIGNED=false`, the server refuses to start unless a pair already exists at those paths. This is the "I insist on a real certificate" mode.

**Half a pair is always a hard error**, in the supplied directory and at the minted-pair location. If only one of `tls.crt`/`tls.key` exists, the server refuses to start rather than mint a replacement.

### The minted certificate is kept in the database too

Every agent package pins the minted certificate, so losing it makes every installed agent fail its handshake until its package is re-applied. The pair is therefore also stored in the database, and the file on disk is a working copy:

| At boot | What happens |
|---|---|
| A pair is on disk | It is served, and the stored copy is updated to match if it differs. |
| Nothing on disk, a pair is stored | The stored pair is written back to disk and served, byte for byte. |
| Nothing anywhere | A pair is minted, stored, written and served. |

- The stored pair is encrypted under `KUBEMG_SECRET_KEY` like other credentials ([Database](database.md#credentials-encrypted-at-rest)). Without that key it is plaintext.
- Only a self-signed pair is kept. A CA-issued certificate is renewed from the CA.
- A stored pair or database that cannot be read **refuses the boot**, rather than minting a new certificate that would re-pin the whole fleet.
- With `KUBEMG_TLS_SELF_SIGNED=false` the stored pair is never used.
- An install upgraded from a version that kept the certificate only on disk copies it into the database on the first boot after the upgrade. Keep the volume across that upgrade.

## `KUBEMG_TLS_*` reference

| Variable | Default | What it does |
|---|---|---|
| `KUBEMG_TLS_ENABLED` | `false` | Terminate HTTPS in this process at all. |
| `KUBEMG_TLS_SUPPLIED_DIR` | `/etc/kubemg/ssl` | Checked first. A recognised pair here wins even over an already-minted pair. |
| `KUBEMG_TLS_CERT_FILE` / `KUBEMG_TLS_KEY_FILE` | `/etc/kubemg/tls/tls.crt` / `tls.key` | Where the minted self-signed pair lives. |
| `KUBEMG_TLS_SELF_SIGNED` | `true` | Mint a self-signed pair when none exists. `false` refuses to start instead. |
| `KUBEMG_TLS_HOSTS` | — (comma-separated) | Extra SANs for a **minted** certificate, beyond the public URL's host and loopback (`localhost`, `127.0.0.1`, `::1`), which are always included. Every name `kubectl`, the console or an agent dials must be covered. |
| `KUBEMG_AGENT_CA_BUNDLE` | — | The chain **agents** must trust to dial this server. See [Agent trust](#agent-trust-the-agent_ca_bundle). Read even when `KUBEMG_TLS_ENABLED=false`. |
| `KUBEMG_ALLOW_INSECURE` | `false` | Allow plaintext HTTP on a non-loopback address. Only for use behind a TLS-terminating proxy. |

## Exact file formats

The self-signed pair kubemg mints is a PEM ECDSA (P-256) certificate, marked as its own CA so it works as a one-certificate trust chain, and an **unencrypted** PEM `EC PRIVATE KEY` (mode `0600`). It is valid for 365 days.

A **supplied** certificate must be PEM with an unencrypted private key. If it is a chain, `tls.crt` holds the **full chain, leaf first**, then intermediates, never the root.

### Recognised filenames

Only these two pairs are recognised, in this order:

| Files | Convention |
|---|---|
| `tls.crt` + `tls.key` | Kubernetes Secret convention |
| `fullchain.pem` + `privkey.pem` | certbot's naming, so a Let's Encrypt live directory mounts as-is |

`tls.crt`/`tls.key` wins if both pairs are present.

### Converting from other formats

??? example "openssl recipes"

    **PKCS#12 / `.pfx` bundle:**

    ```bash
    openssl pkcs12 -in cert.pfx -clcerts -nokeys -out tls.crt
    openssl pkcs12 -in cert.pfx -nocerts -nodes -out tls.key   # -nodes: unencrypted key
    ```

    **DER-encoded certificate** (binary `.cer`/`.crt`):

    ```bash
    openssl x509 -inform der -in certificate.cer -out tls.crt
    ```

    **Encrypted private key** (kubemg never prompts for a passphrase):

    ```bash
    openssl rsa -in encrypted.key -out tls.key
    # or, for an EC key:
    openssl ec -in encrypted.key -out tls.key
    ```

    **Intermediate chain into a full chain**, leaf first:

    ```bash
    cat leaf.crt intermediate.crt > tls.crt
    ```

    **Check certificate and key match** before mounting them (the usual cause of "half a pair" or "certificate does not load"). Both hashes must be equal:

    ```bash
    openssl x509 -noout -modulus -in tls.crt | openssl md5
    openssl rsa  -noout -modulus -in tls.key | openssl md5
    # EC key: compare the public point with `openssl ec -noout -text -in tls.key`
    ```

## Where to mount it

=== "Docker Compose"

    ```yaml
    volumes:
      - tls-certs:/etc/kubemg/tls          # working copy of the minted pair
      - ./ssl:/etc/kubemg/ssl:ro           # your own certificate, if supplied
    ```

    `./ssl` is a plain bind mount so dropping a file in from the host is a
    `cp` and a restart — see [Docker Compose](docker-compose.md#using-a-real-certificate).

=== "Kubernetes"

    The Helm chart mounts the minted pair's working copy on a memory-backed
    `emptyDir` — there is no volume for it, because the database holds the
    copy that matters. Your own certificate is a `kubernetes.io/tls` Secret
    (cert-manager's output fits as-is), named in `tls.existingSecret` and
    mounted read-only at `/etc/kubemg/ssl`:

    ```bash
    helm upgrade --install kubemg oci://ghcr.io/kubemg/charts/kubemg \
      --reuse-values --set tls.existingSecret=kubemg-tls
    ```

    See [Kubernetes](kubernetes.md#tls) for the modes. The container runs as
    uid `65532`; the chart mounts the Secret group-readable under the pod's
    `fsGroup`, so a supplied key is readable without anything else set.

## Permission failures

The server runs **unprivileged** (uid `65532` in the container). A supplied key left at certbot's default (`0600`, owned by root) fails to load with:

```
cannot read the certificate in /etc/kubemg/ssl: permission denied (the server
runs unprivileged; the files have to be readable by uid 65532)
```

Fix it with `chmod 644` on the certificate and `640`/`644` on the key.

## Behind an ingress or load balancer

- **Passthrough**: the proxy forwards the raw TLS stream and kubemg terminates it. One certificate in the path, nothing changes for agent trust. This is the simpler option ([Kubernetes](kubernetes.md#tls)).
- **Terminated at the proxy**: the proxy presents its own certificate and forwards plaintext to kubemg (`KUBEMG_TLS_ENABLED=false` plus `KUBEMG_ALLOW_INSECURE=true`). Agents verify the proxy's certificate, which kubemg never sees, so you must set `KUBEMG_AGENT_CA_BUNDLE` (next section).

## Agent trust: the `AGENT_CA_BUNDLE`

`KUBEMG_AGENT_CA_BUNDLE` is a path to a PEM chain. When set, it is embedded in **every rendered agent install package** (the `bastion-ca` key of the agent's Secret). The agent adds it to its system trust roots; it does not replace them.

| Situation | What agents need |
|---|---|
| Self-signed certificate minted by kubemg | Nothing. It is pinned into every agent package automatically. |
| Certificate from a public CA (Let's Encrypt, DigiCert, ...) | Nothing. Deliberately **not** pinned, so a renewal with a new key does not strand installed agents. |
| Internal/corporate PKI, terminated by kubemg | **Set `KUBEMG_AGENT_CA_BUNDLE`** to the PKI's root/intermediate chain. |
| TLS terminated by an ingress or load balancer | **Set `KUBEMG_AGENT_CA_BUNDLE`** to the chain *the proxy* presents. |

The bundle is **validated at boot**: kubemg refuses to start if the file holds no PEM certificate. A wrong bundle would otherwise fail every agent's handshake with an x509 error that points at the target cluster.

`KUBEMG_BASTION_INSECURE_SKIP_VERIFY` on the agent exists only for running an agent by hand against a dev server. It logs a warning and is not a substitute for a correct bundle.

## Renewal

An existing certificate is never overwritten, minted or supplied. A new supplied certificate is picked up **when the container next starts**; the directory is read once at boot, so a certbot renewal hook must restart the container.

The minted pair is valid for 365 days and nothing rotates it. To replace it, delete the stored copy **and** the files, then restart. This mints a different certificate, so re-apply every agent's install package. Deleting only the files restores the old pair from the database.

```sql
DELETE FROM server_secrets WHERE name = 'tls_self_signed_pair';
```

On Kubernetes the files go with the pod, so that statement and `kubectl rollout restart deployment/kubemg` are the whole procedure. With Docker Compose, also remove `tls.crt` and `tls.key` from the `tls-certs` volume.

## Verification

Check what is served:

```bash
openssl s_client -connect kubemg.example.com:8443 -showcerts </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates
```

Check the health endpoint (`-k` if self-signed and not pinned locally):

```bash
curl -k https://kubemg.example.com:8443/health
# {"status":"ok"}
```

Check that transport works at all. A `401` here, rather than a connection error, means TLS is fine:

```bash
curl -k -H "Authorization: Bearer invalid" https://kubemg.example.com:8443/api/v1/auth/me
```

### Agent x509 failure

The agent logs `dial bastion: x509: certificate signed by unknown authority`. Causes, most likely first:

1. **A self-signed certificate was replaced without re-pinning.** Re-render and re-apply the agent's install package.
2. **TLS is terminated where kubemg cannot see it** (ingress, corporate PKI) and `KUBEMG_AGENT_CA_BUNDLE` is unset. Set it and re-render the package.
3. **The certificate's SANs do not cover the address the agent dials.** Add the host to `KUBEMG_TLS_HOSTS` (minted) or reissue the supplied certificate.

## Next

- [Environment reference](environment.md)
- [Kubernetes](kubernetes.md) — ingress and passthrough in context
- [Docker Compose](docker-compose.md) — the `ssl/` directory in context
