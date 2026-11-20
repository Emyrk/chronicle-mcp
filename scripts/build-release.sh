#!/bin/sh
# Build raw, CGO-free release executables; never publish from this script.
set -eu
version=${1:?usage: build-release.sh vX.Y.Z output-directory}
out=${2:?usage: build-release.sh vX.Y.Z output-directory}
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || {
  echo 'release version must be a stable vX.Y.Z tag' >&2; exit 1;
}
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)
source_version=$(sed -n 's/^const Version = "\([^"]*\)"$/\1/p' "$root/internal/relay/relay.go")
[ "$version" = "v$source_version" ] || {
  echo 'release tag must match internal/relay.Version' >&2; exit 1;
}
mkdir -p "$out"
out=$(CDPATH='' cd -- "$out" && pwd -P)
cd "$root"
for os in linux darwin; do
  for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=true \
      -ldflags='-s -w' -o "$out/chronicle-mcp-$os-$arch" ./cmd/chronicle-mcp
  done
done
