// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

package synthetic

import (
	"context"
	"testing"
	"time"

	"github.com/ninsun-labs/tetragon-bridge/pkg/server"
	bridgev1 "github.com/ninsun-labs/tetragon-bridge/proto/v1"
)

// TestInjectProcessExec verifies that calls to InjectProcessExec end
// up on the hub once Run has wired one in.
func TestInjectProcessExec(t *testing.T) {
	hub := server.NewHub(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subCh, _ := hub.SubscribeProcessExec(ctx, "test")

	src := New("node-1", 0)
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, hub) }()

	// Wait for Run to attach the hub before injecting.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		src.mu.Lock()
		attached := src.hub != nil
		src.mu.Unlock()
		if attached {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	src.InjectProcessExec(&bridgev1.ProcessExec{NodeName: "from-test"})
	select {
	case ev := <-subCh:
		if ev.GetNodeName() != "from-test" {
			t.Errorf("NodeName = %q", ev.GetNodeName())
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber never received the injected event")
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned err = %v", err)
	}
}

// TestLoopHeartbeat exercises the periodic heartbeat path.
func TestLoopHeartbeat(t *testing.T) {
	hub := server.NewHub(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subCh, _ := hub.SubscribeProcessExec(ctx, "test")

	src := New("node-2", 25*time.Millisecond)
	go func() { _ = src.Run(ctx, hub) }()

	select {
	case ev := <-subCh:
		if ev.GetProcess().GetPod().GetName() != "synthetic-loop" {
			t.Errorf("loop emitted Pod=%+v", ev.GetProcess().GetPod())
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat never fired")
	}
}
