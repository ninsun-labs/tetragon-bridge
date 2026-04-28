# Contributing

## Sign-off

Every commit must carry a `Signed-off-by` trailer (DCO):

```sh
git commit -s -m "feat(plugin): your change"
```

## Style

- English only, technical, concise.
- Comments state the non-obvious why in one line.
- Imperative godoc (no `we`/`our`/`let's`).
- `gofumpt` formatted, `golangci-lint` clean.

## CI

Every PR runs build + test + lint. A release is cut by tagging
`v<x.y.z>` on `main` — the release workflow signs the image with
cosign keyless and pushes to GHCR.
