#!/usr/bin/env bash
# Local mirror of .github/workflows/ci.yml - run before every push.
# Mirrors the ugallu hack/ci-local.sh contract: green here = green
# on GitHub Actions.

set -euo pipefail

cd "$(dirname "$0")/.."

# Pin protoc + plugin versions; CI mirrors these in .github/workflows/ci.yml.
PROTOC_VERSION="${PROTOC_VERSION:-25.1}"
PROTOC_GEN_GO_VERSION="${PROTOC_GEN_GO_VERSION:-v1.36.11}"
PROTOC_GEN_GO_GRPC_VERSION="${PROTOC_GEN_GO_GRPC_VERSION:-v1.6.1}"

green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
red()   { printf '\033[0;31m%s\033[0m\n' "$*"; }
section() { printf '\n\033[1;33m==> %s\033[0m\n' "$*"; }

# Resolve pinned protoc into .cache/protoc/<version>/ so the local check
# uses the same toolchain CI uses, no system pollution.
ensure_protoc() {
  local dest=".cache/protoc/${PROTOC_VERSION}"
  if [[ -x "${dest}/bin/protoc" ]]; then
    PROTOC_BIN="${PWD}/${dest}/bin/protoc"
    return 0
  fi
  if ! command -v curl >/dev/null || ! command -v unzip >/dev/null; then
    return 1
  fi
  mkdir -p "${dest}"
  local uname_m; uname_m="$(uname -m)"
  local arch
  case "${uname_m}" in
    x86_64) arch="linux-x86_64" ;;
    aarch64|arm64) arch="linux-aarch_64" ;;
    *) return 1 ;;
  esac
  curl -sSL -o "${dest}/protoc.zip" \
    "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-${arch}.zip"
  unzip -o -q "${dest}/protoc.zip" -d "${dest}"
  chmod +x "${dest}/bin/protoc"
  PROTOC_BIN="${PWD}/${dest}/bin/protoc"
}

# Pinned go plugins installed under GOBIN-style cache so we don't touch
# the user's $GOBIN.
ensure_go_plugins() {
  local cache=".cache/protoc-plugins"
  mkdir -p "${cache}"
  GOBIN="${PWD}/${cache}" go install \
    "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}"
  GOBIN="${PWD}/${cache}" go install \
    "google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}"
  PROTOC_PLUGIN_PATH="${PWD}/${cache}"
}

section "proto-generated drift (proto/v1/*.pb.go)"
if ensure_protoc && ensure_go_plugins; then
  tmp="$(mktemp -d)"
  cp proto/v1/bridge.proto "$tmp/"
  PATH="${PROTOC_PLUGIN_PATH}:${PATH}" "${PROTOC_BIN}" -I=. \
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
  echo "curl / unzip / go missing; skipping (CI will catch it)"
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
