# TLS and certificates

TLS in front of kubemg is not a hardening option — it is a functional
requirement. `client-go` (the Kubernetes client library `kubectl` and every
generated kubeconfig use) **refuses to send a bearer token over plain
`http://`**, full stop. Without TLS, `kubectl exec`, `kubectl proxy`, and
every generated kubeconfig simply fail, with no server-side setting able to
fix it. This page covers how kubemg terminates TLS, exactly what file formats
it expects, how agents decide what to trust, and how to verify all of it.

## Why the server refuses to start without it

`backend/cmd/server/main.go` enforces this at boot, not just in documentation:

- If `KUBEMG_TLS_ENABLED=true`, the server serves HTTPS on `KUBEMG_LISTEN_ADDR`
  and that's the end of it.
- If TLS is disabled and the listen address is **not** loopback
  (`127.0.0.1`/`localhost`, or empty host binding every interface), the
  server refuses to start:

  ```
  refusing to serve plaintext HTTP on :8080: it is reachable from more than
  this machine, and every session JWT would transit unencrypted. Set
  KUBEMG_TLS_ENABLED=true, bind KUBEMG_LISTEN_ADDR to loopback (e.g.
  127.0.0.1:8080), or set KUBEMG_ALLOW_INSECURE=true to start anyway
  ```

- A **loopback** bind without TLS is allowed with a warning, because that's
  how the dev stack runs the backend from the host network in some setups and
  nothing off-box can intercept it. But `kubectl` still won't work against it
  — client-go's refusal doesn't care whether the listener is reachable from
  elsewhere, only that the URL scheme is `http://`.
- `KUBEMG_ALLOW_INSECURE=true` is the explicit override for a
  reachable-from-anywhere plaintext bind. It exists for exactly one situation:
  **a reverse proxy or ingress in front of kubemg that terminates TLS itself**
  and forwards plaintext internally. Using it anywhere else means every
  session JWT and every proxied `kubectl` call transits unencrypted between
  the proxy and kubemg.

## The three ways a certificate ends up in force

kubemg resolves what to serve in a fixed order, checked at every boot:

1. **A supplied certificate wins, unconditionally** — `KUBEMG_TLS_SUPPLIED_DIR`
   (default `/etc/kubemg/ssl`), checked first.
2. If nothing is there and `KUBEMG_TLS_SELF_SIGNED=true` (the default), a
   self-signed pair is minted at `KUBEMG_TLS_CERT_FILE`/`KUBEMG_TLS_KEY_FILE`
   (default `/etc/kubemg/tls/tls.{crt,key}`) — **but only once**. An existing
   pair at those paths is never regenerated or overwritten.
3. If nothing is supplied and `KUBEMG_TLS_SELF_SIGNED=false`, the server
   refuses to start unless a pair already exists at `KUBEMG_TLS_CERT_FILE`/
   `KUBEMG_TLS_KEY_FILE` — this is the "I insist on a real certificate" mode.

**Half a pair is always a hard error**, in both the supplied directory and
the minted-pair location — never a silent fallback. If only one of `tls.crt`/
`tls.key` exists, the server refuses to start rather than mint a self-signed
replacement or serve the certificate without a key: an operator who mounted a
certificate believes it is the one in force, and a silent fallback would pin
that fallback into every rendered agent install package handed out next.

## `KUBEMG_TLS_*` reference

| Variable | Default | What it does |
|---|---|---|
| `KUBEMG_TLS_ENABLED` | `false` | Terminate HTTPS in this process at all. |
| `KUBEMG_TLS_SUPPLIED_DIR` | `/etc/kubemg/ssl` | Checked first, ahead of everything else. A recognised pair here wins even over an already-minted self-signed pair. |
| `KUBEMG_TLS_CERT_FILE` / `KUBEMG_TLS_KEY_FILE` | `/etc/kubemg/tls/tls.crt` / `tls.key` | Where the minted self-signed pair lives, and the explicit paths for an install configured to point directly at a specific file pair instead of a directory. |
| `KUBEMG_TLS_SELF_SIGNED` | `true` | Mint a self-signed pair when neither of the above holds one. `false` refuses to start instead — the stricter posture for an install that must never silently fall back to a self-signed certificate. |
| `KUBEMG_TLS_HOSTS` | — (comma-separated) | Extra SANs for a **minted** certificate, beyond the public URL's host and loopback (`localhost`, `127.0.0.1`, `::1`), which are always included. Every hostname or IP that `kubectl`, the console, or an agent will dial must be covered or the handshake fails on that name. |
| `KUBEMG_AGENT_CA_BUNDLE` | — | The chain **agents** must trust to dial this server. See [Agent trust](#agent-trust-the-agent_ca_bundle) below — read independently of `KUBEMG_TLS_ENABLED`, because it answers a different question than "does this process serve HTTPS." |
| `KUBEMG_ALLOW_INSECURE` | `false` | Override the refusal to serve plaintext HTTP on a non-loopback address. Only correct behind a TLS-terminating proxy. |

## Exact file formats

The self-signed pair kubemg mints is:

- Certificate: PEM-encoded, ECDSA (P-256), self-signed, marked as its own CA
  (`IsCA: true`) so it can act as its own trust anchor — this is what lets it
  be handed to an agent as a complete trust chain of one certificate.
- Key: PEM-encoded, **unencrypted** EC private key (`EC PRIVATE KEY` block),
  file mode `0600`.
- Validity: 365 days from generation minus one hour of clock skew tolerance.

A **supplied** certificate has the same two requirements — PEM, unencrypted
private key — plus one more: if it's a chain rather than a single leaf
certificate, `tls.crt` must contain the **full chain in order, leaf first**,
followed by any intermediates. kubemg loads it with `tls.LoadX509KeyPair`,
the same mechanism any standard TLS server uses, so the ordering rule is the
usual one: leaf, then intermediate(s), never the root CA.

### Recognised filenames

Exactly two filename pairs are recognised, checked in this order, and nothing
else is scanned for:

| Files | Convention |
|---|---|
| `tls.crt` + `tls.key` | Kubernetes Secret convention — what the documentation asks for |
| `fullchain.pem` + `privkey.pem` | certbot's naming — a Let's Encrypt live directory can be mounted as-is, without renaming anything |

`tls.crt`/`tls.key` wins if both pairs happen to be present, so which
certificate is served never depends on directory listing order.

### Converting from other formats

**From a PKCS#12/.pfx bundle** (common when a certificate came from a Windows
CA or a vendor that only hands out `.pfx`):

```bash
# Extract the certificate (and any chain) — no -nodes here, so this stays
# encrypted at rest until the next step decrypts it on the way out.
openssl pkcs12 -in cert.pfx -clcerts -nokeys -out tls.crt

# Extract the private key, unencrypted (kubemg requires an unencrypted key).
openssl pkcs12 -in cert.pfx -nocerts -nodes -out tls.key
```

**From a DER-encoded certificate** (`.cer`/`.crt` with binary content instead
of `-----BEGIN CERTIFICATE-----`):

```bash
openssl x509 -inform der -in certificate.cer -out tls.crt
```

**From an encrypted ("password-protected") private key** — kubemg will not
prompt for a passphrase, so it must be decrypted once, on disk, ahead of time:

```bash
openssl rsa -in encrypted.key -out tls.key
# or, for an EC key:
openssl ec -in encrypted.key -out tls.key
```

**Concatenating an intermediate chain into a full chain**, leaf first:

```bash
cat leaf.crt intermediate.crt > tls.crt
```

**Verifying a certificate and key actually match** before mounting them —
this is the single most common cause of a "half a pair" or "certificate does
not load" failure at boot:

```bash
openssl x509 -noout -modulus -in tls.crt | openssl md5
openssl rsa  -noout -modulus -in tls.key | openssl md5
# (or `openssl ec -noout -text -in tls.key` for an EC key, comparing the
# public point rather than a modulus)
```
Both commands must print the same hash.

## Where to mount it

=== "Docker Compose"

    ```yaml
    volumes:
      - tls-certs:/etc/kubemg/tls          # the minted pair — back this up
      - ./ssl:/etc/kubemg/ssl:ro           # your own certificate, if supplied
    ```

    `./ssl` is a plain bind mount so dropping a file in from the host is a
    `cp` and a restart — see [Docker Compose](docker-compose.md#using-a-real-certificate).

=== "Kubernetes"

    ```yaml
    volumeMounts:
      - name: tls
        mountPath: /etc/kubemg/tls        # PVC — the minted pair
      - name: tls-supplied
        mountPath: /etc/kubemg/ssl
        readOnly: true                    # Secret of type kubernetes.io/tls
    ```

    See [Kubernetes](kubernetes.md#namespace-secret-pvcs) for the full Secret
    and PVC definitions. The container runs as uid `65532` — files in a
    supplied Secret or ConfigMap are readable by any uid by default, but a
    bind-mounted host directory (outside Kubernetes) or a file copied in with
    a restrictive mode is not: see the permission note below.

## Permission failures

The server runs **unprivileged** (uid `65532` in the container). A supplied
certificate whose key is left at certbot's default root-only mode (`0600`
owned by `root`) fails to load, and kubemg says exactly that rather than a
generic TLS error:

```
cannot read the certificate in /etc/kubemg/ssl: permission denied (the server
runs unprivileged; the files have to be readable by uid 65532)
```

Fix it with `chmod 644` on the certificate and a mode the container's uid can
read on the key (`640`/`644` is fine — the key isn't a secret to the process
reading it, only to everyone else on the host).

## Behind an ingress or load balancer

Two shapes are both supported, and they answer the agent-trust question
differently:

- **Passthrough** — the proxy forwards the raw TLS stream and kubemg
  terminates it. There is exactly one certificate anywhere in the path, and
  nothing about the trust story below changes. This is the simpler option and
  the one described in [Kubernetes](kubernetes.md#exposing-it-and-what-that-means-for-tls).
- **Terminated at the proxy** — the proxy presents its own certificate to the
  world and forwards plaintext to kubemg (`KUBEMG_TLS_ENABLED=false` +
  `KUBEMG_ALLOW_INSECURE=true` on kubemg itself). Here, the certificate an
  **agent** verifies when it dials in is the proxy's, which this process never
  sees — nothing here can infer that chain automatically, unlike the
  self-signed case where kubemg knows exactly what it minted. This is what
  `KUBEMG_AGENT_CA_BUNDLE` exists for; see the next section.

## Agent trust: the `AGENT_CA_BUNDLE`

Every agent dials the bastion over TLS and has to decide whether to trust the
certificate it's presented. `KUBEMG_AGENT_CA_BUNDLE` is a path to a PEM chain
that, when set, is embedded into **every rendered agent install package**
(as the `bastion-ca` key of the agent's Kubernetes Secret, read by the agent
as `KUBEMG_BASTION_CA`) — the agent *adds* this to its system trust roots
rather than replacing them.

Whether it's needed, and what goes in it, depends on the certificate:

| Situation | What agents need |
|---|---|
| Self-signed certificate, minted by kubemg | Nothing to set — kubemg automatically pins its own self-signed certificate into every rendered agent package. |
| Certificate from a publicly-trusted CA (Let's Encrypt, DigiCert, etc.) | Nothing — deliberately **not** pinned, because pinning it would strand every already-installed agent the moment that certificate is renewed with a new key. Public CAs are already in every OS's trust store. |
| Internal/corporate PKI, terminated by kubemg itself | **Set `KUBEMG_AGENT_CA_BUNDLE`** to that PKI's root/intermediate chain. An internal PKI is not self-signed in the sense kubemg's own detection covers, and nothing about this process's certificate can tell kubemg "this chain isn't public" — you have to say so. |
| TLS terminated by an ingress or load balancer in front of kubemg | **Set `KUBEMG_AGENT_CA_BUNDLE`** to whatever chain *that proxy* presents — agents verify the proxy's certificate, not kubemg's, and this process has no visibility into what the proxy serves. Read even when `KUBEMG_TLS_ENABLED=false` here, for exactly this reason. |

The bundle is **validated at boot**: kubemg refuses to start if the file
doesn't parse as at least one PEM certificate. A wrong bundle would otherwise
strand the entire fleet with every agent failing its handshake with an x509
error that points at the *target cluster*, which is a much harder thing to
debug than a refusal to boot here.

The agent's own escape hatch, `KUBEMG_BASTION_INSECURE_SKIP_VERIFY`, exists
only for hand-running an agent against a dev bastion and logs a warning when
used — it is not a substitute for getting the CA bundle right in a real
install.

## Renewal

**An existing certificate is never overwritten** — this applies to both the
minted pair and a supplied one. A certificate you drop into the supplied
directory is picked up **the next time the container starts**, not live:
that directory is read once at boot, so a certbot renewal hook (or your CI/CD
renewal pipeline) has to restart the container after replacing the files.
Nothing here watches the directory for changes.

For the minted self-signed pair: it is valid for 365 days. Nothing rotates it
automatically either — if you're relying on it past a year, delete the pair
from the `tls-certs` volume/PVC deliberately and restart, which mints a fresh
one and will require re-pinning every agent (a fresh certificate is a
different certificate).

## Verification

**Check what's actually being served:**

```bash
openssl s_client -connect kubemg.example.com:8443 -showcerts </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates
```

**Confirm the health endpoint answers over HTTPS** (add `-k` if the
certificate is self-signed and you haven't pinned it locally):

```bash
curl -k https://kubemg.example.com:8443/health
# {"status":"ok"}
```

**Confirm `kubectl`-shaped calls work at all** — the fastest sanity check
that TLS, not RBAC or the tunnel, is the problem:

```bash
curl -k -H "Authorization: Bearer invalid" https://kubemg.example.com:8443/api/v1/auth/me
# a 401 here (rather than a connection error or client-go refusing outright)
# means the transport is fine and the failure, if any, is somewhere else
```

**What an agent's x509 failure looks like**, and which knob fixes it. The
agent logs something like:

```
dial bastion: x509: certificate signed by unknown authority
```

That means the agent's trust store — system roots plus whatever
`KUBEMG_BASTION_CA` gave it — doesn't cover the certificate it was presented.
Fixes, in order of likelihood:

1. **A self-signed certificate rotated without re-pinning.** Re-render and
   re-apply the agent's install package so it picks up the current
   `bastion-ca`.
2. **TLS is terminated somewhere this process can't see** (an ingress, a
   corporate PKI) and `KUBEMG_AGENT_CA_BUNDLE` was never set. Set it to that
   chain and re-render the agent package.
3. **The certificate's SANs don't cover the address the agent dials.** Add
   the missing host to `KUBEMG_TLS_HOSTS` (for a minted certificate) or
   reissue the supplied certificate with the right SAN.

## Next

- [Environment reference](environment.md)
- [Kubernetes](kubernetes.md) — ingress and passthrough in context
- [Docker Compose](docker-compose.md) — the `ssl/` directory in context
