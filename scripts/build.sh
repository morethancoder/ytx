#!/usr/bin/env bash
# Build ./ytx, or `go install` it with the arg "install".
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
ldflags="-s -w -X github.com/morethancoder/ytx/internal/cli.Version=${version}"
if [ "${1:-}" = "install" ]; then
  go install -ldflags "$ldflags" ./cmd/ytx
  ok "installed ytx ${version} into $(go env GOBIN || true)$( [ -z "$(go env GOBIN)" ] && echo "$(go env GOPATH)/bin")"
else
  go build -ldflags "$ldflags" -o ytx ./cmd/ytx
  ok "built ./ytx ${version}"
fi
