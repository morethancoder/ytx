#!/usr/bin/env bash
# lint: gofmt + vet. default: lint + offline tests. live: tests tagged `live` (real YouTube).
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
unformatted=$(gofmt -l . || true)
[ -z "$unformatted" ] || die "gofmt needed on: $unformatted"
go vet ./... && ok "gofmt + vet clean"
[ "${1:-}" = "lint" ] && exit 0
export YTX_NO_KEYCHAIN=1
if [ "${1:-}" = "live" ]; then
  YTX_HOME="$(mktemp -d)"; export YTX_HOME
  trap 'rm -rf "$YTX_HOME"' EXIT
  go test -count=1 -tags live -run Live ./... && ok "live tests passed"
  exit 0
fi
race=""; [ -n "${RACE:-}" ] && race="-race"
go test $race ./... && ok "offline tests passed"
