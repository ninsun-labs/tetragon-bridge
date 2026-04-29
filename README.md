# tetragon-bridge

gRPC bridge that fronts the [Tetragon](https://tetragon.io) Hubble export
and re-exposes process / network / syscall events as a typed Subscribe
stream consumable by [`ninsun-labs/ugallu`](https://github.com/ninsun-labs/ugallu)
operators (`tenant-escape`, `dns-detect` Tetragon-fallback,
`seccomp-gen` training capture).

The bridge runs as an in-cluster DaemonSet next to Tetragon. Tetragon
stays upstream-vanilla; the bridge adds:
- typed event streams (`StreamProcessExec`, `StreamDNSQuery`,
  `StreamFileOpen`) — operators only see what they need
- per-subscriber bounded ring + drop-oldest-first backpressure
- bearer-token auth interceptor
- per-subscriber egress rate limit (token bucket)
- a built-in `synthetic` source for lab/dev clusters that don't have
  Tetragon installed yet

## Compatibility matrix

| Bridge version | Tetragon | Notes |
|---|---|---|
| 0.0.1-alpha.1 | ≥ 1.0 | scaffold, no-op gRPC server |
| 0.1.0 | ≥ 1.0, ≤ 1.x | typed Subscribe streams + synthetic source |

## Build + ship

Distributed as an OCI image:

```
ghcr.io/ninsun-labs/tetragon-bridge:<version>
```

Multi-arch (amd64 + arm64). Cosign-signed keyless via GitHub OIDC
+ Fulcio + Rekor on every release tag (`v*`).

The companion Helm sub-chart `tetragon-bridge` (also in this repo)
is consumable as a dependency from the ugallu umbrella chart.

## License

Apache-2.0 — see [LICENSE](LICENSE).

## Contributing

Sign-off required (DCO). See [CONTRIBUTING.md](CONTRIBUTING.md).
