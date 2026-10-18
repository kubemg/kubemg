#!/bin/sh
# Builds the kubemg CLI for every platform it is published for, as the archives
# a release carries and a SHA256SUMS over them.
#
#   hack/cli-dist.sh VERSION [OUT]     (OUT is relative to cli/, default dist)
#
# One script for `make cli-dist` and the release workflow, so what a developer
# builds and what a tag publishes are the same files. Pure Go with CGO off: a
# cross-build is the same binary a native one would be.
set -eu

VERSION=${1:?usage: hack/cli-dist.sh VERSION [OUT]}
OUT=${2:-dist}
PLATFORMS=${CLI_PLATFORMS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"}

cd "$(dirname "$0")/../cli"
rm -rf "$OUT"
mkdir -p "$OUT"

for target in $PLATFORMS; do
  os=${target%/*}
  arch=${target#*/}
  ext=""
  [ "$os" = windows ] && ext=".exe"
  name="kubemg_${VERSION}_${os}_${arch}"
  mkdir -p "$OUT/$name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" \
    -o "$OUT/$name/kubemg$ext" ./cmd/kubemg
  cp LICENSE "$OUT/$name/"
  if [ "$os" = windows ]; then
    (cd "$OUT" && zip -qr "$name.zip" "$name")
  else
    tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
  fi
  rm -rf "${OUT:?}/$name"
done

(cd "$OUT" && sha256sum kubemg_* > SHA256SUMS)
echo "built $(ls "$OUT" | grep -c '^kubemg_') archives in cli/$OUT"
