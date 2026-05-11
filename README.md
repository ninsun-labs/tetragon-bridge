# tetragon-bridge

<p align="center">
  <a href="https://github.com/ninsun-labs/tetragon-bridge/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/ninsun-labs/tetragon-bridge/ci.yml?branch=main&label=ci&logo=github&style=flat-square"></a>
  <a href="https://github.com/ninsun-labs/tetragon-bridge/releases"><img alt="Release" src="https://img.shields.io/github/v/release/ninsun-labs/tetragon-bridge?include_prereleases&label=release&style=flat-square&color=00e5ff"></a>
  <a href="https://github.com/ninsun-labs/tetragon-bridge/blob/main/LICENSE.md"><img alt="License" src="https://img.shields.io/github/license/ninsun-labs/tetragon-bridge?style=flat-square"></a>
  <a href="https://goreportcard.com/report/github.com/ninsun-labs/tetragon-bridge"><img alt="Go Report" src="https://goreportcard.com/badge/github.com/ninsun-labs/tetragon-bridge?style=flat-square"></a>
  <a href="https://pkg.go.dev/github.com/ninsun-labs/tetragon-bridge"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/ninsun-labs/tetragon-bridge.svg"></a>
  <a href="https://ugallu.io"><img alt="Docs" src="https://img.shields.io/badge/docs-ugallu.io-00e5ff?style=flat-square"></a>
  <br>
  <a href="https://github.com/ninsun-labs/tetragon-bridge/stargazers"><img alt="Stars" src="https://img.shields.io/github/stars/ninsun-labs/tetragon-bridge?style=flat-square&color=00e5ff"></a>
  <a href="https://github.com/ninsun-labs/tetragon-bridge/issues"><img alt="Issues" src="https://img.shields.io/github/issues/ninsun-labs/tetragon-bridge?style=flat-square"></a>
  <a href="https://github.com/ninsun-labs/tetragon-bridge/commits/main"><img alt="Last commit" src="https://img.shields.io/github/last-commit/ninsun-labs/tetragon-bridge?style=flat-square"></a>
  <a href="https://github.com/sigstore/cosign"><img alt="Signed by cosign" src="https://img.shields.io/badge/signed-cosign%20keyless-00e5ff?style=flat-square&logo=sigstore"></a>
  <a href="https://tetragon.io"><img alt="Tetragon" src="https://img.shields.io/badge/tetragon-%E2%89%A51.0-FF6D70?style=flat-square"></a>
</p>

gRPC bridge that fronts the upstream
[Tetragon](https://tetragon.io) Hubble export and re-exposes
process, DNS-query, and file-open events as a small, typed
`Subscribe` surface tailored to the
[`ninsun-labs/ugallu`](https://github.com/ninsun-labs/ugallu)
operators (`tenant-escape`, `dns-detect` Tetragon-fallback,
`seccomp-gen` training capture).

Tetragon stays upstream-vanilla; the bridge adds typed event
streams, per-subscriber bounded ring buffers with
drop-oldest-first backpressure, bearer-token auth, and a built-in
synthetic source for lab clusters that don't have Tetragon
installed yet.

> **Status:** `v0.1.0` (first functional release). Pre-`v1.0.0`
> minor versions may break compat.

## Capability matrix

| Capability | v0.1.0 |
|---|---|
| Typed gRPC streams (`StreamProcessExec`, `StreamDNSQuery`, `StreamFileOpen`) | yes |
| Per-subscriber ring buffer with drop-oldest-first | yes (default 10000) |
| Bearer-token authentication (`Authorization: Bearer <tok>`) | yes |
| Per-subscriber egress rate limit (token bucket) | yes |
| Synthetic source for clusters without Tetragon | yes |
| Tetragon source (Hubble GetEvents adapter) | yes |
| Plain TCP fallback (dev / lab only) | yes |
| TLS server certificate | yes |
| mTLS (client cert verification) | roadmap |
| Multi-arch image (amd64 + arm64) | yes |
| Cosign-keyless signature on every tag | yes |

## Compatibility

| Bridge version | Tetragon | Notes |
|---|---|---|
| 0.1.0 | >= 1.0 (tested), <= 1.x (best-effort) | Hubble `GetEvents` surface has been stable since 1.0 |

Each release pins the tested Tetragon version range. Outside the
declared range the bridge may compile but is not validated.

## gRPC surface

Three server-streaming RPCs are exposed in
`ninsun.tetragon_bridge.v1`:

- `StreamProcessExec(SubscribeRequest) returns (stream ProcessExec)`
  emits one `ProcessExec` per `PROCESS_EXEC` event observed by
  Tetragon (after node-local rate limiting).
- `StreamDNSQuery(SubscribeRequest) returns (stream DNSQuery)`
  emits one `DNSQuery` per kprobe-observed DNS lookup. Used by
  `dns-detect` when its primary CoreDNS plugin backend is
  unreachable (degraded mode).
- `StreamFileOpen(SubscribeRequest) returns (stream FileOpen)`
  emits one `FileOpen` per `open()` syscall. Filtered server-side
  by `Subject.target_pod`; unfiltered subscribes are rejected
  on a node-local DaemonSet because the volume would saturate
  the bridge.

See [`proto/v1/bridge.proto`](proto/v1/bridge.proto) for the full
contract.

## Deploy

Distributed as a multi-arch OCI image:

```
ghcr.io/ninsun-labs/tetragon-bridge:v0.1.0
```

The companion Helm sub-chart `tetragon-bridge` (consumable as a
dependency from the
[ugallu umbrella chart](https://github.com/ninsun-labs/ugallu/tree/main/charts/ugallu))
runs the bridge as a DaemonSet next to Tetragon and exposes the
gRPC service inside the cluster.

## Verifying a release

Every image is signed via GitHub OIDC + Fulcio + Rekor. To
verify:

```bash
cosign verify \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/ninsun-labs/tetragon-bridge/.+' \
  ghcr.io/ninsun-labs/tetragon-bridge:v0.1.0
```

## License

[Apache-2.0](LICENSE.md). DCO sign-off required on every commit
(`git commit -s`).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
