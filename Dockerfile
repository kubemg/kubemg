# The KubeMG management plane as one image: the console built into the binary
# that serves the API it calls.
#
# One image rather than two is a deployment decision, and the reason is the
# install this exists for. An air-gapped site mirrors artefacts by hand, and the
# whole product is three of them — this image, postgres, and the agent. A
# separate console container would add a fourth, a second version to keep in
# step, a CORS origin to configure and an ingress path split to get wrong, and
# would buy nothing: the console and the gateway are released together because
# they are one product.
#
# This file lives at the repository root because it is the one build that spans
# both modules. `backend/Dockerfile.dev` and `frontend/Dockerfile.dev` are
# untouched and still what `make up` runs.

# --- console -----------------------------------------------------------------
# Pinned to the builder's architecture. The output is JavaScript, so building it
# under emulation for a target platform would cost minutes to produce the
# identical bundle.
FROM --platform=$BUILDPLATFORM node:22-alpine AS console

WORKDIR /src

# Lockfile first, so a source-only change reuses the installed layer.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci

COPY frontend/ ./
# `npm run build` is `tsc -b && vite build`: a type error fails the image rather
# than shipping a console that was never type-checked.
RUN npm run build

# --- server ------------------------------------------------------------------
# Same approach as agent/Dockerfile: pinned to the builder, cross-compiled to the
# target. Pure Go with CGO off produces the same binary either way, and QEMU
# would only make it slower.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

# The console lands where pkg/webui embeds from. In a source checkout that
# directory holds only a .gitkeep and the binary serves the API alone; here it
# holds a real build, so the console ships inside the binary.
COPY --from=console /src/dist ./pkg/webui/assets

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFLAGS=-trimpath go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/kubemg ./cmd/server

# Both directories are created here because the runtime image has no shell to
# create them in. They are the two pieces of state the server writes: the TLS
# material it mints on first boot, and the session recordings. Mount a volume
# over each — an unmounted recordings directory means replays that vanish on the
# next deploy, and unmounted TLS means a fresh self-signed certificate that
# every already-installed agent has pinned the previous version of.
RUN mkdir -p /state/etc/kubemg/tls /state/var/lib/kubemg/recordings

# --- runtime -----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="KubeMG" \
      org.opencontainers.image.description="Centralized, audited access to every Kubernetes cluster — without opening one firewall port" \
      org.opencontainers.image.source="https://github.com/kubemg/kubemg" \
      org.opencontainers.image.licenses="AGPL-3.0"

COPY --from=build /out/kubemg /kubemg
COPY --from=build --chown=65532:65532 /state/etc/kubemg /etc/kubemg
COPY --from=build --chown=65532:65532 /state/var/lib/kubemg /var/lib/kubemg

# Matches the nonroot user the base image ships.
USER 65532:65532

# TLS is the default here, unlike the plaintext default in config.go: that
# default serves a dev stack, and this image is what gets deployed. client-go
# refuses to send a bearer token over http, so a plaintext bastion cannot serve
# a generated kubeconfig or an exec session at all.
ENV KUBEMG_LISTEN_ADDR=:8443 \
    KUBEMG_TLS_ENABLED=true \
    KUBEMG_TLS_SELF_SIGNED=true \
    GIN_MODE=release

EXPOSE 8443

ENTRYPOINT ["/kubemg"]
