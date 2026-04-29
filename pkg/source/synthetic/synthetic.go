// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

// Package synthetic is a Source that emits hand-crafted events for
// tests + lab smoke. It exposes Inject* helpers tests use to drive
// the bridge through a known event sequence without needing a real
// Tetragon DaemonSet on the node.
//
// In production the bridge runs with the tetragon Source instead; the
// flag --source=synthetic on the CLI selects this implementation
// (see cmd/tetragon-bridge/main.go) for development clusters that
// don't have Tetragon installed yet.
package synthetic

import (
	"context"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ninsun-labs/tetragon-bridge/pkg/server"
	bridgev1 "github.com/ninsun-labs/tetragon-bridge/proto/v1"
)

// Source pumps any event handed to InjectXxx into the bridge Hub.
// Concurrent InjectXxx calls are safe.
type Source struct {
	// NodeName stamps every emitted event with the node identifier;
	// usually the kubelet's spec.nodeName.
	NodeName string

	// LoopInterval, when non-zero, makes Run emit a small synthetic
	// heartbeat (ProcessExec on a fake "synthetic-loop" Pod) at the
	// configured cadence. Useful for the lab smoke that wants to
	// observe a steady event flow without producing the events
	// itself.
	LoopInterval time.Duration

	mu  sync.Mutex
	hub *server.Hub
}

// New returns a Source ready to attach via Run.
func New(nodeName string, loopInterval time.Duration) *Source {
	return &Source{NodeName: nodeName, LoopInterval: loopInterval}
}

// Name implements source.Source.
func (s *Source) Name() string { return "synthetic" }

// Run captures hub for the lifetime of ctx; ticker emits the
// optional heartbeat. Returns nil on ctx cancel.
func (s *Source) Run(ctx context.Context, hub *server.Hub) error {
	s.mu.Lock()
	s.hub = hub
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.hub = nil
		s.mu.Unlock()
	}()

	if s.LoopInterval <= 0 {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(s.LoopInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			hub.PublishProcessExec(&bridgev1.ProcessExec{
				Timestamp: timestamppb.Now(),
				NodeName:  s.NodeName,
				Process: &bridgev1.ProcessInfo{
					BinaryPath: "/usr/local/bin/synthetic-loop",
					Pod:        &bridgev1.PodRef{Namespace: "ugallu-system", Name: "synthetic-loop"},
				},
				Argv: []string{"synthetic-loop", "--heartbeat"},
			})
		}
	}
}

// InjectProcessExec publishes ev to the attached hub. No-op when Run
// has not yet attached.
func (s *Source) InjectProcessExec(ev *bridgev1.ProcessExec) {
	s.mu.Lock()
	hub := s.hub
	s.mu.Unlock()
	if hub == nil {
		return
	}
	hub.PublishProcessExec(ev)
}

// InjectDNSQuery — see InjectProcessExec.
func (s *Source) InjectDNSQuery(ev *bridgev1.DNSQuery) {
	s.mu.Lock()
	hub := s.hub
	s.mu.Unlock()
	if hub == nil {
		return
	}
	hub.PublishDNSQuery(ev)
}

// InjectFileOpen — see InjectProcessExec.
func (s *Source) InjectFileOpen(ev *bridgev1.FileOpen) {
	s.mu.Lock()
	hub := s.hub
	s.mu.Unlock()
	if hub == nil {
		return
	}
	hub.PublishFileOpen(ev)
}
