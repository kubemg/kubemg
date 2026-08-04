# The agent is the open-source half of KubeMG and it runs in someone else's
# cluster, so the image is built to be boring: a static binary on a distroless
# base, no shell, non-root, nothing to audit but the binary itself.
# The build stage is pinned to the *builder's* architecture and cross-compiles
# to the target, rather than being emulated on it. The agent is pure Go with
# CGO off, so a cross-build is the same binary a native one would produce —
# while building it under QEMU costs minutes per architecture for nothing.
# BUILDPLATFORM/TARGETOS/TARGETARCH are supplied by buildx; the defaults keep a
# plain `docker build` (which sets neither) working and native.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

# Dependencies first so a source-only change reuses the cached module layer.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# CGO off and the symbol table stripped: a fully static ~10 MB binary that needs
# no libc in the runtime image.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFLAGS=-trimpath go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/kubemg-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="KubeMG Dumb Agent" \
      org.opencontainers.image.description="Outbound reverse tunnel from a Kubernetes cluster to a KubeMG bastion" \
      org.opencontainers.image.source="https://github.com/kubemg/kubemg" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/kubemg-agent /kubemg-agent

# Matches the runAsUser in the deployment manifest.
USER 65532:65532
EXPOSE 8081

ENTRYPOINT ["/kubemg-agent"]
