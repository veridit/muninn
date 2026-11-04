#!/usr/bin/env bash
# Build release archives for every supported platform (pure Go, CGO off).
# Usage: scripts/build-release.sh v0.1.0   -> dist/muninn_<ver>_<os>_<arch>.{tar.gz,zip} + SHA256SUMS
set -euo pipefail
cd "$(dirname "$0")/.."
ver="${1:?version}"
rm -rf dist && mkdir -p dist
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${t%/*}; arch=${t#*/}
  name="muninn_${ver#v}_${os}_${arch}"
  work="dist/$name"; mkdir -p "$work"
  exe=muninn; [ "$os" = windows ] && exe=muninn.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$ver" -o "$work/$exe" ./cmd/muninn
  cp LICENSE README.md "$work/"
  if [ "$os" = windows ]; then (cd dist && zip -qr "$name.zip" "$name"); else tar -C dist -czf "dist/$name.tar.gz" "$name"; fi
  rm -rf "$work"
done
(cd dist && shasum -a 256 * > SHA256SUMS)
ls -la dist
