#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

if [[ ! -f .env ]]; then
  echo "error: .env file not found" >&2
  exit 1
fi

TOKEN="$(sed -n 's/^TOKEN[[:space:]]*=[[:space:]]*//p' .env | head -n 1)"
if [[ -z "$TOKEN" ]]; then
  echo "error: TOKEN is not set in .env" >&2
  exit 1
fi

mkdir -p bin
LDFLAGS="-X main.buildToken=${TOKEN}"

build() {
  local goos="$1"
  local goarch="$2"
  local output="$3"

  echo "building ${output}"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
    -ldflags "$LDFLAGS" \
    -o "bin/${output}" \
    .
}

build linux amd64 sbb_rt_linux_amd64
build windows amd64 sbb_rt_windows_amd64.exe
build darwin amd64 sbb_rt_macos_amd64
build darwin arm64 sbb_rt_macos_arm64

echo "builds written to ${ROOT_DIR}/bin"
