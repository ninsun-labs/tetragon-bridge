// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

// Command tetragon-bridge fronts the Tetragon Hubble export and
// re-exposes process / network / syscall events as a typed Subscribe
// gRPC stream consumable by ugallu operators (tenant-escape,
// seccomp-gen, dns-detect Tetragon-fallback).
//
// Source selection:
//
//	--source=tetragon   real Tetragon FineGuidanceSensors stream
//	                    (default). Dials --tetragon-endpoint.
//	--source=synthetic  hand-crafted events for lab/dev clusters
//	                    that don't have Tetragon installed yet.
//	                    Accepts injection via the --synthetic-loop
//	                    cadence (heartbeat) only — REST/CLI injection
//	                    is intentionally not exposed in v0.1.0.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/ninsun-labs/tetragon-bridge/pkg/server"
	"github.com/ninsun-labs/tetragon-bridge/pkg/source"
	syntheticsrc "github.com/ninsun-labs/tetragon-bridge/pkg/source/synthetic"
	tetragonsrc "github.com/ninsun-labs/tetragon-bridge/pkg/source/tetragon"
	bridgev1 "github.com/ninsun-labs/tetragon-bridge/proto/v1"
)

const version = "v0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tetragon-bridge:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		listenAddr       string
		probeAddr        string
		bearerTokenFile  string
		bufferSize       int
		defaultRate      uint
		sourceKind       string
		tetragonEndpoint string
		nodeName         string
		syntheticLoop    time.Duration
	)
	flag.StringVar(&listenAddr, "listen-addr", ":50051", "gRPC listen address")
	flag.StringVar(&probeAddr, "probe-addr", ":8080", "HTTP /healthz + /readyz address")
	flag.StringVar(&bearerTokenFile, "bearer-token-file", "", "Path to a file containing the shared-secret bearer token (empty disables auth)")
	flag.IntVar(&bufferSize, "per-subscriber-buffer", server.DefaultPerSubscriberBuffer, "Per-subscriber ring depth")
	flag.UintVar(&defaultRate, "default-events-per-sec", server.DefaultDefaultMaxEventsPerSec, "Default per-subscriber egress rate cap")
	flag.StringVar(&sourceKind, "source", "tetragon", "Event source: tetragon | synthetic")
	flag.StringVar(&tetragonEndpoint, "tetragon-endpoint", "unix://"+tetragonsrc.DefaultUnixSocket, "Tetragon endpoint (unix:// or host:port)")
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Node name stamped on every event (defaults to NODE_NAME env)")
	flag.DurationVar(&syntheticLoop, "synthetic-loop", 5*time.Second, "When --source=synthetic, emit a heartbeat ProcessExec at this interval (0 disables)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("tetragon-bridge starting", "version", version, "listenAddr", listenAddr, "source", sourceKind, "nodeName", nodeName)

	if nodeName == "" {
		// Without a node name every emitted event is under-attributed;
		// fail closed so the operator notices in CrashLoopBackOff.
		return errors.New("--node-name (or NODE_NAME env) is required")
	}

	bearerToken := ""
	if bearerTokenFile != "" {
		b, err := os.ReadFile(bearerTokenFile)
		if err != nil {
			return fmt.Errorf("read bearer token file: %w", err)
		}
		bearerToken = strings.TrimSpace(string(b))
	}

	srv := server.NewServer(&server.ServerConfig{
		BearerToken:            bearerToken,
		PerSubscriberBuffer:    bufferSize,
		DefaultMaxEventsPerSec: uint32(defaultRate), //nolint:gosec // CLI flag clamped by server-side defaults
	})

	src, err := buildSource(sourceKind, tetragonEndpoint, nodeName, syntheticLoop)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the source first so events queue up while the gRPC
	// server is binding the listener.
	srcDone := make(chan error, 1)
	go func() { srcDone <- src.Run(ctx, srv.Hub) }()

	gs, healthSrv, lis, err := startGRPC(listenAddr, srv)
	if err != nil {
		return err
	}
	defer gs.GracefulStop()

	probeStop := startProbe(probeAddr, healthSrv, log)
	defer probeStop()

	// Mark serving once everything is wired.
	healthSrv.SetServingStatus("tetragon-bridge", healthpb.HealthCheckResponse_SERVING)

	// Block on whichever fires first: signal, source error, gRPC
	// listener error.
	gsDone := make(chan error, 1)
	go func() { gsDone <- gs.Serve(lis) }()

	select {
	case <-ctx.Done():
		log.Info("shutdown requested")
	case err := <-srcDone:
		if err != nil {
			log.Error("source exited with error", "err", err)
			return err
		}
		log.Info("source exited cleanly")
	case err := <-gsDone:
		if err != nil {
			log.Error("grpc server exited with error", "err", err)
			return err
		}
	}
	return nil
}

// buildSource resolves the --source flag.
func buildSource(kind, tetragonEndpoint, nodeName string, syntheticLoop time.Duration) (source.Source, error) {
	switch kind {
	case "tetragon":
		return tetragonsrc.New(tetragonsrc.Config{Endpoint: tetragonEndpoint, NodeName: nodeName})
	case "synthetic":
		return syntheticsrc.New(nodeName, syntheticLoop), nil
	default:
		return nil, fmt.Errorf("unknown --source=%q", kind)
	}
}

// startGRPC binds the listener and registers the bridge + the gRPC
// healthz service.
func startGRPC(addr string, srv *server.Server) (*grpc.Server, *health.Server, net.Listener, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("listen: %w", err)
	}
	opts := []grpc.ServerOption{}
	if interceptor := srv.AuthInterceptor(); interceptor != nil {
		opts = append(opts, grpc.StreamInterceptor(interceptor))
	}
	gs := grpc.NewServer(opts...)
	bridgev1.RegisterTetragonBridgeServer(gs, srv)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(gs, healthSrv)
	return gs, healthSrv, lis, nil
}

// startProbe runs an HTTP server with /healthz + /readyz that mirror
// the gRPC health service. Returns a stop func the caller defers.
func startProbe(addr string, healthSrv *health.Server, log *slog.Logger) func() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		// Defer to the gRPC health service: serving == ready.
		resp, err := healthSrv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: "tetragon-bridge"})
		if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	probe := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := probe.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("probe server", "err", err)
		}
	}()
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = probe.Shutdown(shutdownCtx)
	}
}
