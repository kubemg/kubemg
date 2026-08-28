# KubeMG's browser shell image.
#
# This is the container an operator's in-console terminal runs in, on their own
# cluster. It is KubeMG's own image rather than a base somebody else maintains,
# and it is deliberately austere:
#
#   * **No package manager.** An operator's shell that can install software is a
#     supply chain, not a terminal. There is no apk, no apt, no pip and no curl —
#     what is in the image at build time is what is in the shell.
#   * **Two tools, pinned.** kubectl and helm, at versions this build names, both
#     verified against the checksum their own release publishes.
#   * **A shell, and the coreutils busybox brings.** Enough to edit a values
#     file, pipe things about and read YAML. Nothing that opens a socket.
#   * **Non-root, on a distroless base.** No shadow file to write to, no setuid
#     binaries, and the pod that runs it drops every capability and mounts no
#     service account token — see deploy/kustomize/base/rbac.yaml.
#
# It holds **no credential**. The kubeconfig arrives at runtime, written into a
# tmpfs home over an exec, and it points at KubeMG's proxy rather than at the
# cluster's API server. See backend/pkg/api/shell.go.

# Versions are build arguments so an air-gapped site can rebuild this against
# whatever it has mirrored without editing the file.
ARG KUBECTL_VERSION=v1.32.1
ARG HELM_VERSION=v3.16.4
ARG BUSYBOX_IMAGE=busybox:1.37.0-musl
ARG FETCH_IMAGE=alpine:3.21
ARG BASE_IMAGE=gcr.io/distroless/static-debian12:nonroot

# ---- the shell itself -------------------------------------------------------
#
# busybox is copied out with relative symlinks rather than installed with
# `busybox --install`, which writes absolute ones pointing at wherever it was
# invoked from — those break the moment the tree is copied to a different path
# in the final image.
FROM ${BUSYBOX_IMAGE} AS busybox
RUN mkdir -p /out/bin \
 && cp /bin/busybox /out/bin/busybox \
 && cd /out/bin \
 && for applet in $(./busybox --list); do \
        [ "$applet" = "busybox" ] || ln -s busybox "$applet"; \
    done

# ---- the two tools ----------------------------------------------------------
#
# The checksum is fetched from the same release the binary came from, which is
# what catches a truncated download or a mirror serving the wrong file. It is
# not independent attestation of the upstream release — nothing fetched over the
# same channel can be — and a site that needs that should build this image from
# artefacts it has already verified, which is what the ARGs above are for.
FROM ${FETCH_IMAGE} AS tools
ARG KUBECTL_VERSION
ARG HELM_VERSION
ARG TARGETARCH
RUN set -eu; \
    apk add --no-cache curl tar; \
    mkdir -p /out; \
    arch="${TARGETARCH:-amd64}"; \
    curl -fsSL -o /tmp/kubectl \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl"; \
    curl -fsSL -o /tmp/kubectl.sha256 \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl.sha256"; \
    echo "$(cat /tmp/kubectl.sha256)  /tmp/kubectl" | sha256sum -c -; \
    install -m 0755 /tmp/kubectl /out/kubectl; \
    curl -fsSL -o /tmp/helm.tgz \
        "https://get.helm.sh/helm-${HELM_VERSION}-linux-${arch}.tar.gz"; \
    curl -fsSL -o /tmp/helm.sha256 \
        "https://get.helm.sh/helm-${HELM_VERSION}-linux-${arch}.tar.gz.sha256sum"; \
    awk '{print $1"  /tmp/helm.tgz"}' /tmp/helm.sha256 | sha256sum -c -; \
    tar -xzf /tmp/helm.tgz -C /tmp; \
    install -m 0755 "/tmp/linux-${arch}/helm" /out/helm

# ---- the image --------------------------------------------------------------
FROM ${BASE_IMAGE}

COPY --from=busybox /out/bin/ /bin/
COPY --from=tools /out/kubectl /usr/local/bin/kubectl
COPY --from=tools /out/helm /usr/local/bin/helm

# HOME and KUBECONFIG both point into the emptyDir the pod mounts, which is the
# only writable path in the container and dies with it. KubeMG writes the
# kubeconfig there once the pod is up; until it does, kubectl has nothing to
# talk to, which is the correct state for a shell nobody has been let into yet.
ENV HOME=/home/shell \
    KUBECONFIG=/home/shell/.kube/config \
    PATH=/usr/local/bin:/bin \
    PAGER=cat \
    TERM=xterm-256color

USER nonroot
WORKDIR /home/shell

ENTRYPOINT ["/bin/sh"]
