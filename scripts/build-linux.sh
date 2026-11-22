#!/usr/bin/env bash
# Builds the Linux binary for local development and testing.
set -euo pipefail

VERSION="${VERSION:-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/runtime"

mkdir -p "$OUT"
cd "$ROOT"
CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "-s -w -X main.version=${VERSION}" \
  -o "$OUT/clippers-live-hub" ./cmd/clipper

ls -lh "$OUT/clippers-live-hub"
