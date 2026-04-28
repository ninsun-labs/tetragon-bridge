# tetragon-bridge

gRPC bridge that fronts the [Tetragon](https://tetragon.io) Hubble export
and re-exposes process / network / syscall events as a typed Subscribe
stream consumable by [`ninsun-labs/ugallu`](https://github.com/ninsun-labs/ugallu)
operators (Wave 3 `tenant-escape`, Wave 4 `seccomp-gen`).

The bridge runs as an in-cluster Deployment, separate from Tetragon
itself. Tetragon stays upstream-vanilla; the bridge adds:
- typed event filters (PROCESS_EXEC, PROCESS_KPROBE_TYPE_SYSCALL,
  network_namespace_change)
- per-subscriber rate limiting + buffered fanout
- mTLS-ready endpoint (Pomerium-style; mTLS-on becomes default in
  a follow-up post-`wave4-final`)

> **Status**: pre-alpha. Skeleton only — no event forwarding yet. The
> Subscribe gRPC + Tetragon-Hubble client wiring lands in v0.1.0
> (Wave 4 Sprint 1 of the upstream ugallu roadmap).

## Compatibility matrix

| Bridge version | Tetragon | Notes |
|---|---|---|
| 0.0.1-alpha.1 | ≥ 1.0 | scaffold, no-op gRPC server |
| 0.1.0 (planned) | ≥ 1.0, ≤ 1.x | full Subscribe stream |

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
