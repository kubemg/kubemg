# KubeMG's browser shell image.
#
# This is the container an operator's in-console terminal runs in, on their own
# cluster. It is KubeMG's own image rather than a base somebody else maintains,
# and it is deliberately austere:
#
#   * **No package manager.** An operator's shell that can install software is a
#     supply chain, not a terminal. There is no apk, no apt, no pip and no curl —
#     what is in the image at build time is what is in the shell.
#   * **One tool, pinned.** kubectl, at the version this build names, compiled
#     from that release's own source archive, verified against the checksum the
#     release publishes. See "the tool" below for why it is compiled rather
#     than downloaded, and when that should stop.
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
ARG KUBECTL_VERSION=v1.37.1
ARG GO_IMAGE=golang:1.26.9-alpine
ARG XNET_VERSION=v0.60.0
ARG BUSYBOX_IMAGE=busybox:1.37.0-musl
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
# kubectl is compiled here rather than downloaded, for the reason helm left this
# image: an upstream release binary carries the Go standard library and
# golang.org/x/net it was built with. When Go ships a security release, every
# kubectl on dl.k8s.io keeps the old one until the next Kubernetes patch.
# v1.37.1 was built on Go 1.26.8 with x/net v0.57.0. The release scan refused
# it for the net/http fixes in Go 1.26.9 and x/net v0.60.0 (GO-2026-6610 to
# 6617), and kubectl cannot be removed the way helm was: it is what the shell
# is for.
#
# So the source is the release's own `kubernetes-src.tar.gz`, checked against
# the sha256 the release publishes beside it, and exactly two things change:
# the Go toolchain (GO_IMAGE) and the x/net module (XNET_VERSION). Nothing else
# in Kubernetes' dependency set moves, and the version kubectl reports is the
# release's own. The checksum catches a truncated download or a mirror serving
# the wrong file. It is not independent attestation of the upstream release —
# nothing fetched over the same channel can be. A site that needs that should
# build this image from artefacts it has already verified, which is what the
# ARGs above are for.
#
# The root module's replace directives point every k8s.io/* import at the
# staging tree, so the build runs in plain module mode (GOWORK=off) rather than
# against vendor/, which the x/net change would make stale.
#
# **Go back to the downloaded binary** once a Kubernetes patch release is built
# on a Go and an x/net the scan accepts. Check with
# `go version -m kubectl`. Compiling a tool somebody else releases is the cost
# of shipping it without known findings, not a preference.
#
# The build runs on the builder's own platform and cross-compiles. kubectl has
# no cgo, so this is the same binary an emulated build would make, in a
# fraction of the time.
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS tools
ARG KUBECTL_VERSION
ARG XNET_VERSION
ARG TARGETOS
ARG TARGETARCH
RUN set -eu; \
    apk add --no-cache curl; \
    mkdir -p /src /out; \
    curl -fsSL -o /tmp/src.tar.gz \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/kubernetes-src.tar.gz"; \
    curl -fsSL -o /tmp/src.tar.gz.sha256 \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/kubernetes-src.tar.gz.sha256"; \
    echo "$(cat /tmp/src.tar.gz.sha256)  /tmp/src.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/src.tar.gz -C /src; \
    rm /tmp/src.tar.gz
WORKDIR /src
ENV GOWORK=off GOFLAGS=-mod=mod CGO_ENABLED=0
RUN go get "golang.org/x/net@${XNET_VERSION}"
RUN set -eu; \
    version="${KUBECTL_VERSION#v}"; \
    major="${version%%.*}"; minor="${version#*.}"; minor="${minor%%.*}"; \
    flags=""; \
    for pkg in k8s.io/client-go/pkg/version k8s.io/component-base/version; do \
        flags="$flags -X $pkg.gitVersion=${KUBECTL_VERSION}"; \
        flags="$flags -X $pkg.gitMajor=$major -X $pkg.gitMinor=$minor"; \
    done; \
    GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-amd64}" \
        go build -trimpath -ldflags "-s -w $flags" -o /out/kubectl ./cmd/kubectl; \
    go version -m /out/kubectl | grep -E "^/out/kubectl: go|golang.org/x/net[[:space:]]"

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
