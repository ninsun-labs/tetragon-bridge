// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

// Package source defines the interface every event producer
// (Tetragon adapter, synthetic injector for tests/lab) implements.
// The bridge's main wires exactly one Source per process; switching
// between them is a flag, not a code path.
package source

import (
	"context"

	"github.com/ninsun-labs/tetragon-bridge/pkg/server"
)

// Source is anything that can pump events into the bridge Hub for the
// lifetime of ctx. Implementations are responsible for reconnecting
// on transient errors; Run returns when ctx is cancelled OR the
// backend has hit a non-recoverable failure.
type Source interface {
	// Name returns the source kind (used in logs + the /healthz
	// extra-info hook).
	Name() string

	// Run forwards events into hub.Publish*. It must return when
	// ctx is cancelled. A nil return is "clean shutdown"; a non-nil
	// error is bubbled to the manager so the process exits non-zero
	// for the kubelet to restart the pod.
	Run(ctx context.Context, hub *server.Hub) error
}
