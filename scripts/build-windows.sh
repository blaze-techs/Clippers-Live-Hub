#!/usr/bin/env bash
# Cross-compiles the Windows executable. ffmpeg is not bundled: drop
# ffmpeg.exe and ffprobe.exe into dist/bin/ next to the app, or have them on PATH.
set -euo pipefail

VERSION="${VERSION:-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/dist"
mkdir -p "$OUT/bin"

cd "$ROOT"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "-s -w -X main.version=${VERSION}" \
  -o "$OUT/ClippersLiveHub.exe" ./cmd/clipper

ls -lh "$OUT/ClippersLiveHub.exe"
echo "built $OUT/ClippersLiveHub.exe (version ${VERSION})"
