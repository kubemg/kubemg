# Air-gapped installs

kubemg fetches nothing from the internet at runtime — the console's fonts are
served from the binary and no page calls an external host. What an air-gapped
site has to arrange is the **images**: the ones this host runs, and the ones it
hands to your clusters in every agent install package.

| Image | Pulled by | Needed for |
| --- | --- | --- |
| `ghcr.io/kubemg/kubemg` | this host | the server and console |
| `postgres:16-alpine` | this host | the Docker Compose path and the Helm chart's evaluation mode — not needed with an external PostgreSQL |
| `ghcr.io/kubemg/kubemg-agent` | **your target clusters** | the agent |
| `ghcr.io/kubemg/kubemg-shell` | **your target clusters** | the [browser shell](../clusters/browser-shell.md) |
| `busybox:1.36` | **your target clusters** | [debug containers](../clusters/terminals-and-logs.md#debugging-a-pod-with-no-shell) |

The last three are pulled by the clusters kubemg manages, not by the kubemg
host, so the registry you put them in has to be reachable **from every one of
those clusters**.

## Getting the images across

**Through a mirror.** If your site has a registry that can pull from the
internet, or you can copy registry-to-registry, mirror the five images under
the same repository paths — `kubemg/kubemg`, `kubemg/kubemg-agent`,
`kubemg/kubemg-shell`, `library/postgres`, `library/busybox`. A
registry-to-registry copy keeps the amd64+arm64 index, so a fleet with both
kinds of node is covered.

**On physical media.** From a checkout of kubemg, on any machine with Docker
and internet access — nothing else is installed, the pull runs in a container:

```bash
make save-images                          # kubemg-images-0.11.1-linux-amd64.tar
make save-images SAVE_PLATFORM=linux/arm64
make save-images KUBEMG_VERSION=0.11.1 AGENT_VERSION=0.11.1 SHELL_VERSION=0.11.1
```

That writes one tarball holding all five images at the versions the checkout
pins (or the ones named), for **one** platform — the one named, not the
machine running it. Carry it across, then load it and push it into your
internal registry:

```bash
docker load -i kubemg-images-0.11.1-linux-amd64.tar
mirror=registry.internal
for pair in \
  ghcr.io/kubemg/kubemg:0.11.1=kubemg/kubemg:0.11.1 \
  ghcr.io/kubemg/kubemg-agent:0.11.1=kubemg/kubemg-agent:0.11.1 \
  ghcr.io/kubemg/kubemg-shell:0.11.1=kubemg/kubemg-shell:0.11.1 \
  busybox:1.36=library/busybox:1.36 \
  postgres:16-alpine=library/postgres:16-alpine; do
  docker tag "${pair%%=*}" "$mirror/${pair#*=}" && docker push "$mirror/${pair#*=}"
done
```

A single-platform tarball pushed this way gives your registry that platform
only. If your clusters run both amd64 and arm64 nodes, use a mirror instead.

## Pointing kubemg at the mirror

**Helm:** `--set global.imageRegistry=registry.internal` rewrites every image
the chart runs *and* the ones the console hands out, keeping each repository
path. See [Kubernetes](kubernetes.md#mirrored-and-air-gapped-registries).

**Docker Compose:** set each image in `.env` — see
[Docker Compose](docker-compose.md#air-gapped-installs).

Either way the three cluster-side images can also be changed later, without a
restart, on the **Agent settings** page.

## A mirror that requires authentication

The pod that pulls the image needs a pull secret, and that pod is on your
cluster. kubemg **names** the Secret and never holds what is in it — registry
credentials stay yours, and a KubeMG compromise does not hand them out.

1. Name the Secret once for the whole install: **Agent settings → Image pull
   secret**, `KUBEMG_AGENT_IMAGE_PULL_SECRET`, or the chart's
   `agent.imagePullSecret`. It is the name of a `docker-registry` Secret in the
   agent namespace, the same name on every cluster.
2. Open the install package for a cluster (the registration wizard, or
   **Agent install** on the cluster's page). With a pull secret configured it
   shows a first step before the install command — creating the namespace and
   the Secret against the registry the agent image is pulled from, reading the
   credentials from `REGISTRY_USERNAME` and `REGISTRY_PASSWORD` in your shell.
   Run it, then the install command.

The agent's Deployment and every browser shell pod name that Secret. An agent
already running keeps what it was installed with until its package is applied
again, the same as an image change.

A **debug container** is the exception, and it is Kubernetes' rule rather
than kubemg's: an ephemeral container is pulled with the pull secrets of the
pod it is added to, and a running pod's cannot be changed. The debug image has
to be pullable with whatever the target pod already carries — in practice, from
a mirror path that allows anonymous pulls.

## The chart itself

Carry the chart across with `helm pull oci://ghcr.io/kubemg/charts/kubemg
--version 0.11.1` and install from the `.tgz`.
