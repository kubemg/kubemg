# Air-gapped installs

kubemg fetches nothing from the internet at runtime. An air-gapped site only
has to arrange the **images**: the ones this host runs and the ones it hands
to your clusters in every agent install package. This page covers mirroring,
carrying them across on media, and pull secrets.

| Image | Pulled by | Needed for |
| --- | --- | --- |
| `ghcr.io/kubemg/kubemg` | this host | the server and console |
| `postgres:16-alpine` | this host | the Docker Compose path and the Helm chart's evaluation mode — not needed with an external PostgreSQL |
| `ghcr.io/kubemg/kubemg-agent` | **your target clusters** | the agent |
| `ghcr.io/kubemg/kubemg-shell` | **your target clusters** | the [browser shell](../clusters/terminals-and-logs.md) |
| `busybox:1.36` | **your target clusters** | [debug containers](../clusters/terminals-and-logs.md#debugging-a-pod-with-no-shell) |

The last three are pulled by your managed clusters, so their registry must be
reachable **from every one of those clusters**.

## Getting the images across

**Through a mirror.** Mirror the five images under the same repository paths:
`kubemg/kubemg`, `kubemg/kubemg-agent`, `kubemg/kubemg-shell`,
`library/postgres`, `library/busybox`. A registry-to-registry copy keeps the
amd64+arm64 index.

**On physical media.** From a kubemg checkout, on any machine with Docker and
internet access (the pull runs in a container):

```bash
make save-images                          # kubemg-images-0.14.0-linux-amd64.tar
make save-images SAVE_PLATFORM=linux/arm64
make save-images KUBEMG_VERSION=0.14.0 AGENT_VERSION=0.14.0 SHELL_VERSION=0.14.0
```

That writes one tarball with all five images at the pinned (or named)
versions, for **one** platform: the one you name, not the machine's. Carry it
across, load it and push it to your registry:

```bash
docker load -i kubemg-images-0.14.0-linux-amd64.tar
mirror=registry.internal
for pair in \
  ghcr.io/kubemg/kubemg:0.14.0=kubemg/kubemg:0.14.0 \
  ghcr.io/kubemg/kubemg-agent:0.14.0=kubemg/kubemg-agent:0.14.0 \
  ghcr.io/kubemg/kubemg-shell:0.14.0=kubemg/kubemg-shell:0.14.0 \
  busybox:1.36=library/busybox:1.36 \
  postgres:16-alpine=library/postgres:16-alpine; do
  docker tag "${pair%%=*}" "$mirror/${pair#*=}" && docker push "$mirror/${pair#*=}"
done
```

This gives your registry that one platform only. For mixed amd64/arm64 nodes,
use a mirror.

## Pointing kubemg at the mirror

**Helm:** `--set global.imageRegistry=registry.internal` rewrites every image,
including the ones the console hands out. See [Kubernetes](kubernetes.md#mirrored-and-air-gapped-registries).

**Docker Compose:** set each image in `.env` — see
[Docker Compose](docker-compose.md#air-gapped-installs).

The three cluster-side images can also be changed later, without a restart, on
the **Agent settings** page.

## A mirror that requires authentication

The pulling pod is on your cluster, so the pull secret is too. kubemg only
**names** the Secret; it never holds the registry credentials.

1. Name the Secret once for the whole install: **Agent settings → Image pull
   secret**, `KUBEMG_AGENT_IMAGE_PULL_SECRET`, or the chart's
   `agent.imagePullSecret`. It is the name of a `docker-registry` Secret in the
   agent namespace, the same name on every cluster.
2. Open the install package for a cluster (the registration wizard, or
   **Agent install** on the cluster's page). With a pull secret configured it
   shows a first step before the install command. It creates the namespace and
   the Secret for the agent image's registry, reading `REGISTRY_USERNAME` and
   `REGISTRY_PASSWORD` from your shell. Run it, then the install command.

The agent's Deployment and every browser shell pod name that Secret. A running
agent keeps its old settings until its package is applied again.

A **debug container** is the exception (Kubernetes' rule): it is pulled with
the pull secrets of the pod it is added to, which cannot be changed. The debug
image must be pullable with what the target pod already carries, in practice
from a mirror path that allows anonymous pulls.

## The chart itself

Carry the chart across with `helm pull oci://ghcr.io/kubemg/charts/kubemg
--version 0.14.0` and install from the `.tgz`.
