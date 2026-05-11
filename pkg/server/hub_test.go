// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"
	"time"

	bridgev1 "github.com/ninsun-labs/tetragon-bridge/proto/v1"
)

// TestHub_FanoutTwoSubscribers verifies that one publish reaches every
// attached subscriber on the same stream.
func TestHub_FanoutTwoSubscribers(t *testing.T) {
	h := NewHub(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, _ := h.SubscribeProcessExec(ctx, "alice")
	b, _ := h.SubscribeProcessExec(ctx, "bob")

	ev := &bridgev1.ProcessExec{NodeName: "node-1"}
	h.PublishProcessExec(ev)

	select {
	case got := <-a:
		if got.GetNodeName() != "node-1" {
			t.Errorf("alice got %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("alice did not receive event")
	}
	select {
	case got := <-b:
		if got.GetNodeName() != "node-1" {
			t.Errorf("bob got %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("bob did not receive event")
	}
}

// TestHub_DropOldestOnFull verifies drop-oldest-first when a
// subscriber's ring is saturated.
func TestHub_DropOldestOnFull(t *testing.T) {
	h := NewHub(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := h.SubscribeProcessExec(ctx, "slow")
	// Saturate without draining.
	for i := 0; i < 5; i++ {
		h.PublishProcessExec(&bridgev1.ProcessExec{NodeName: "node-1"})
	}

	// Buffer has capacity 2 - at most 2 events readable. The third
	// publish onward must have incremented drops.
	snap := h.Snapshot()
	var drops uint64
	for _, s := range snap {
		if s.SubscriberID == "slow" {
			drops = s.Drops
		}
	}
	if drops == 0 {
		t.Fatal("expected drops > 0 on full ring")
	}
	if got := len(ch); got > 2 {
		t.Errorf("buffered = %d, ring size 2", got)
	}
}

// TestHub_CtxCancelDetaches asserts that context cancellation removes
// the subscriber from the hub bookkeeping.
func TestHub_CtxCancelDetaches(t *testing.T) {
	h := NewHub(4)
	ctx, cancel := context.WithCancel(context.Background())
	_, _ = h.SubscribeProcessExec(ctx, "ephemeral")
	if got := len(h.Snapshot()); got != 1 {
		t.Fatalf("Snapshot() before cancel = %d, want 1", got)
	}
	cancel()
	// The cancel goroutine runs asynchronously; loop until it drains.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(h.Snapshot()) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber still attached after ctx cancel: %+v", h.Snapshot())
}

// TestHub_DuplicateSubscriberIdsDoNotStealBuffers verifies the
// id-suffix logic in subscribe(): two subscribers with the same id
// get independent rings.
func TestHub_DuplicateSubscriberIdsDoNotStealBuffers(t *testing.T) {
	h := NewHub(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, _ := h.SubscribeProcessExec(ctx, "dupe")
	b, _ := h.SubscribeProcessExec(ctx, "dupe")

	h.PublishProcessExec(&bridgev1.ProcessExec{NodeName: "node-1"})

	for i, ch := range []<-chan *bridgev1.ProcessExec{a, b} {
		select {
		case got := <-ch:
			if got == nil {
				t.Errorf("ch[%d] received nil", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("ch[%d] did not receive event - duplicate ids stole each other's buffer", i)
		}
	}
}
