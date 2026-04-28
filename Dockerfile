# Skeleton multi-stage build. v0.1.0 wires the real Tetragon-Hubble
# client + gRPC server.
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' \
    -o /out/tetragon-bridge ./cmd/tetragon-bridge

FROM scratch
COPY --from=builder /out/tetragon-bridge /tetragon-bridge

LABEL org.opencontainers.image.title="tetragon-bridge"
LABEL org.opencontainers.image.description="gRPC bridge fronting Tetragon Hubble export for ugallu operators"
LABEL org.opencontainers.image.source="https://github.com/ninsun-labs/tetragon-bridge"
LABEL org.opencontainers.image.licenses="Apache-2.0"

USER 65532:65532
ENTRYPOINT ["/tetragon-bridge"]
