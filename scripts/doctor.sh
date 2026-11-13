#!/usr/bin/env bash
# Check the tools ytx needs to build and run, then ask ytx itself.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
require_cli go "brew install go"
command -v yt-dlp >/dev/null && ok "yt-dlp $(yt-dlp --version)" || warn "yt-dlp missing (only \`ytx download\` needs it): uv tool install yt-dlp"
command -v ffmpeg >/dev/null && ok "ffmpeg found" || warn "ffmpeg missing (only \`ytx download\` merges need it): brew install ffmpeg"
go run ./cmd/ytx doctor --human
