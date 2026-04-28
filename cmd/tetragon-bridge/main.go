// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

// Command tetragon-bridge fronts the Tetragon Hubble export and
// re-exposes process / network / syscall events as a typed Subscribe
// gRPC stream consumable by ugallu operators (tenant-escape,
// seccomp-gen). Skeleton only — the gRPC server + Tetragon-Hubble
// client wiring lands in v0.1.0.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
)

const version = "v0.0.1-alpha.1"

func main() {
	var listenAddr string
	flag.StringVar(&listenAddr, "listen-addr", ":50051", "gRPC listen address (skeleton: ignored)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("tetragon-bridge starting (skeleton)", "version", version, "listenAddr", listenAddr)

	// TODO(v0.1.0): boot gRPC server, dial Tetragon Hubble, fan out
	// typed events via Subscribe stream. For now, exit cleanly so
	// the placeholder image is healthy under kubelet's livenessProbe.
	fmt.Println("tetragon-bridge skeleton ok")
}
