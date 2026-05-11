#!/usr/bin/env bash
# Local mirror of .github/workflows/ci.yml - run before every push.
# Mirrors the ugallu hack/ci-local.sh contract: green here = green
# on GitHub Actions.

set -euo pipefail

cd "$(dirname "$0")/.."

green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
red()   { printf '\033[0;31m%s\033[0m\n' "$*"; }
section() { printf '\n\033[1;33m==> %s\033[0m\n' "$*"; }

section "proto-generated drift (proto/v1/*.pb.go)"
if command -v protoc >/dev/null && command -v protoc-gen-go >/dev/null && command -v protoc-gen-go-grpc >/dev/null; then
  tmp="$(mktemp -d)"
  cp proto/v1/bridge.proto "$tmp/"
  protoc -I=. \
    --go_out="$tmp" --go_opt=paths=source_relative \
    --go-grpc_out="$tmp" --go-grpc_opt=paths=source_relative \
    proto/v1/bridge.proto
  for f in bridge.pb.go bridge_grpc.pb.go; do
    if ! diff -q "$tmp/proto/v1/$f" "proto/v1/$f" >/dev/null; then
      red "drift detected in proto/v1/$f - re-run protoc"
      diff "$tmp/proto/v1/$f" "proto/v1/$f" | head -20
      exit 1
    fi
  done
  rm -rf "$tmp"
  green "no drift in proto/v1/*.pb.go"
else
  echo "protoc / protoc-gen-go(_grpc) missing; skipping (CI will catch it)"
fi

section "build"
go build ./...

section "test (race)"
go test -race -timeout 120s ./...

section "lint (golangci-lint)"
# Burn the cache up front - local cache survives across runs and can
# mask issues when config or linter version drifts (golangci/golangci-lint#5414).
if command -v golangci-lint >/dev/null; then
  golangci-lint cache clean >/dev/null
  golangci-lint run --timeout=5m ./...
else
  echo "golangci-lint not installed; skipping (CI will catch it)"
fi

echo
green "=== CI-LOCAL: ALL GREEN ==="
