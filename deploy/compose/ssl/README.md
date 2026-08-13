# Put a real certificate here

This directory is mounted read-only into the container at `/etc/kubemg/ssl`, and
it is the first place the server looks for the certificate it serves. Leave it
empty and a self-signed one is minted on first boot instead — which works, and
which every agent install package then pins, so replacing it later means
re-issuing that package for every cluster.

Two filename pairs are recognised, in this order:

| Files | Where they come from |
| --- | --- |
| `tls.crt` + `tls.key` | the Kubernetes convention, and what to rename to |
| `fullchain.pem` + `privkey.pem` | certbot — a Let's Encrypt live directory can be mounted here as it stands |

Nothing else is read. A directory scanned for "something that looks like a
certificate" is one where renaming a file quietly changes what the bastion
serves, so the names are fixed and anything else is ignored.

```bash
cp /path/to/fullchain.pem ssl/tls.crt
cp /path/to/privkey.pem   ssl/tls.key
chmod 644 ssl/tls.crt ssl/tls.key
docker compose restart kubemg
```

Three things are worth knowing before that restart:

- **The server runs unprivileged** (uid `65532`), so the files have to be
  readable by it. A key left at certbot's root-only `0600` stops the boot with a
  message that says exactly that, rather than falling back to the self-signed
  certificate — an operator who mounted a certificate believes it is the one in
  force, and a silent fallback would pin the fallback into the install packages
  they then hand out.
- **A certificate found here wins** over the generated pair in the `tls-certs`
  volume. That is deliberate: on any install past its first boot the generated
  one still exists, and letting it keep winning would mean watching a real
  certificate be ignored with nothing saying why.
- **It has to cover the address clusters dial** — the host in
  `KUBEMG_PUBLIC_URL`. A certificate valid for a name nothing connects to fails
  the handshake as surely as no certificate at all.

Half a pair, or a certificate and key that do not go together, also stops the
boot. Both are misconfigurations that would otherwise surface as a TLS error on
somebody else's screen.

Once it is in place, Settings → Deployment says so — it reads the certificate the
running server actually serves, not what this directory contains.
