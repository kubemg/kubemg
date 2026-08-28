# KubeMG's browser shell image.
#
# This is the container an operator's in-console terminal runs in, on their own
# cluster. It is KubeMG's own image rather than a base somebody else maintains,
# and it is deliberately austere:
#
#   * **No package manager.** An operator's shell that can install software is a
#     supply chain, not a terminal. There is no apk, no apt, no pip and no curl —
#     what is in the image at build time is what is in the shell.
#   * **One tool, pinned.** kubectl, at the version this build names, verified
#     against the checksum its own release publishes.
#
#     helm was here and was deliberately removed. Its upstream release binaries
#     carry the Go standard library they were built against, which runs behind
#     Go's own security releases for weeks at a time — nine CRITICAL/HIGH at the
#     last count, none of them reachable by any version bump because the newest
#     release of every helm line carried the same ones. The three ways out were
#     to accept them in an ignore file, to build helm from source against a
#     current toolchain, or not to ship it; the first parks known findings in
#     the image an operator's shell runs, and the second means shipping a helm
#     nobody else has. KubeMG's console already installs, upgrades (chart
#     version included), rolls back and uninstalls releases through the
#     impersonated tunnel, so what left with the binary is the part the console
#     deliberately does not do — see docs/clusters/browser-shell.md, which says
#     what that is rather than leaving it to be discovered at a prompt.
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
ARG KUBECTL_VERSION=v1.37.0
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

# ---- the tool ---------------------------------------------------------------
#
# The checksum is fetched from the same release the binary came from, which is
# what catches a truncated download or a mirror serving the wrong file. It is
# not independent attestation of the upstream release — nothing fetched over the
# same channel can be — and a site that needs that should build this image from
# artefacts it has already verified, which is what the ARGs above are for.
FROM ${FETCH_IMAGE} AS tools
ARG KUBECTL_VERSION
ARG TARGETARCH
RUN set -eu; \
    apk add --no-cache curl; \
    mkdir -p /out; \
    arch="${TARGETARCH:-amd64}"; \
    curl -fsSL -o /tmp/kubectl \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl"; \
    curl -fsSL -o /tmp/kubectl.sha256 \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl.sha256"; \
    echo "$(cat /tmp/kubectl.sha256)  /tmp/kubectl" | sha256sum -c -; \
    install -m 0755 /tmp/kubectl /out/kubectl

# ---- the image --------------------------------------------------------------
FROM ${BASE_IMAGE}

COPY --from=busybox /out/bin/ /bin/
COPY --from=tools /out/kubectl /usr/local/bin/kubectl

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
