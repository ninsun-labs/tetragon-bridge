// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

// Package server is the gRPC implementation of the TetragonBridge
// service. It wires three concerns: a fanout Hub that holds the
// per-subscriber bounded queues, a bearer-token interceptor that
// guards the listener, and a per-subscriber token-bucket rate
// limiter applied at egress.
//
// Hub is independent of the upstream Tetragon source — the Hubble
// adapter (cmd/tetragon-bridge wires it in pkg/hubble) calls
// PublishProcessExec / PublishDNSQuery / PublishFileOpen as events
// arrive. Subscribers obtain a channel via the matching Subscribe*
// helper and consume until ctx cancellation.
package server

import (
	"context"
	"sync"
	"sync/atomic"

	bridgev1 "github.com/ninsun-labs/tetragon-bridge/proto/v1"
)

// DefaultPerSubscriberBuffer is the bounded ring depth used when the
// caller leaves the corresponding ServerConfig field zero. Sized to
// absorb a 10s burst at 1000 events/sec without dropping for a
// well-behaved subscriber.
const DefaultPerSubscriberBuffer = 10000

// Hub fans every published event to the matching set of subscribers.
// Every Subscribe* call returns a bounded channel and a cancel func
// that detaches it; on overflow the oldest event is dropped and the
// per-subscriber drop counter is incremented (read by the metrics
// package).
type Hub struct {
	mu sync.RWMutex

	procExec map[string]*subscriber[*bridgev1.ProcessExec]
	dnsQuery map[string]*subscriber[*bridgev1.DNSQuery]
	fileOpen map[string]*subscriber[*bridgev1.FileOpen]

	bufSize int
}

type subscriber[T any] struct {
	id        string
	ch        chan T
	drops     atomic.Uint64
	cancel    func()
	closeOnce sync.Once
}

// NewHub returns an empty Hub with the configured ring depth. Pass
// 0 to use DefaultPerSubscriberBuffer.
func NewHub(bufSize int) *Hub {
	if bufSize <= 0 {
		bufSize = DefaultPerSubscriberBuffer
	}
	return &Hub{
		procExec: map[string]*subscriber[*bridgev1.ProcessExec]{},
		dnsQuery: map[string]*subscriber[*bridgev1.DNSQuery]{},
		fileOpen: map[string]*subscriber[*bridgev1.FileOpen]{},
		bufSize:  bufSize,
	}
}

// SubscribeProcessExec attaches a new subscriber. The returned
// channel receives events until detach() runs (return value 2) or
// the parent ctx is cancelled. id is used in metrics + log fields;
// duplicates are tolerated (each instance gets its own buffer).
func (h *Hub) SubscribeProcessExec(ctx context.Context, id string) (<-chan *bridgev1.ProcessExec, func()) {
	return subscribe(ctx, h, h.procExec, id)
}

// SubscribeDNSQuery — see SubscribeProcessExec.
func (h *Hub) SubscribeDNSQuery(ctx context.Context, id string) (<-chan *bridgev1.DNSQuery, func()) {
	return subscribe(ctx, h, h.dnsQuery, id)
}

// SubscribeFileOpen — see SubscribeProcessExec.
func (h *Hub) SubscribeFileOpen(ctx context.Context, id string) (<-chan *bridgev1.FileOpen, func()) {
	return subscribe(ctx, h, h.fileOpen, id)
}

// PublishProcessExec fans ev out to all attached subscribers. Drops
// for full subscribers are counted but never block the publisher.
func (h *Hub) PublishProcessExec(ev *bridgev1.ProcessExec) { publish(h, h.procExec, ev) }

// PublishDNSQuery — see PublishProcessExec.
func (h *Hub) PublishDNSQuery(ev *bridgev1.DNSQuery) { publish(h, h.dnsQuery, ev) }

// PublishFileOpen — see PublishProcessExec.
func (h *Hub) PublishFileOpen(ev *bridgev1.FileOpen) { publish(h, h.fileOpen, ev) }

// SubscriberSnapshot is one row of telemetry the metrics endpoint
// exports per active subscriber.
type SubscriberSnapshot struct {
	Stream      string
	SubscriberID string
	Buffered    int
	Drops       uint64
}

// Snapshot returns a point-in-time view of every subscriber across
// all three streams. Callers iterate and emit metrics; ordering is
// not stable (map iteration).
func (h *Hub) Snapshot() []SubscriberSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]SubscriberSnapshot, 0, len(h.procExec)+len(h.dnsQuery)+len(h.fileOpen))
	for _, s := range h.procExec {
		out = append(out, SubscriberSnapshot{Stream: "process_exec", SubscriberID: s.id, Buffered: len(s.ch), Drops: s.drops.Load()})
	}
	for _, s := range h.dnsQuery {
		out = append(out, SubscriberSnapshot{Stream: "dns_query", SubscriberID: s.id, Buffered: len(s.ch), Drops: s.drops.Load()})
	}
	for _, s := range h.fileOpen {
		out = append(out, SubscriberSnapshot{Stream: "file_open", SubscriberID: s.id, Buffered: len(s.ch), Drops: s.drops.Load()})
	}
	return out
}

// subscribe is the generic Subscribe* implementation. The hub pointer
// is passed explicitly because Go method generics can't yet target
// generic struct fields.
func subscribe[T any](ctx context.Context, h *Hub, table map[string]*subscriber[T], id string) (<-chan T, func()) {
	s := &subscriber[T]{id: id, ch: make(chan T, h.bufSize)}
	key := id
	h.mu.Lock()
	// Allow duplicate ids by appending a counter suffix — operators
	// that crash-loop and reconnect should not silently steal each
	// other's buffers.
	for i := 0; ; i++ {
		if _, taken := table[key]; !taken {
			break
		}
		key = id + "#" + itoa(i)
	}
	table[key] = s
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		if cur, ok := table[key]; ok && cur == s {
			delete(table, key)
		}
		h.mu.Unlock()
		// Closing the channel after delete is safe: publish takes the
		// read lock + filters by table membership before sending.
		// closeOnce makes cancel idempotent so the user's defer + the
		// ctx watcher goroutine can both call it without panicking.
		s.closeOnce.Do(func() { close(s.ch) })
	}
	s.cancel = cancel

	// Wire ctx-driven detach so subscribers don't have to remember to
	// call the cancel func themselves.
	go func() {
		<-ctx.Done()
		cancel()
	}()

	return s.ch, cancel
}

// publish fans ev out across the table. The drop-oldest-first
// behaviour matches design 21 §D2.3: a slow subscriber loses
// information but the publisher never blocks.
func publish[T any](h *Hub, table map[string]*subscriber[T], ev T) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range table {
		select {
		case s.ch <- ev:
		default:
			// Ring full: drop the oldest (incrementing the per-
			// subscriber drop counter) and try the new one again.
			// drop-oldest-first matches design 21 §D2.3 — the
			// publisher never blocks.
			select {
			case <-s.ch:
				s.drops.Add(1)
			default:
			}
			select {
			case s.ch <- ev:
			default:
				// Subscriber is quiescent (no receiver, no slot freed
				// even after pop). Count an extra drop so the gap
				// shows up on telemetry.
				s.drops.Add(1)
			}
		}
	}
}

// itoa is a tiny strconv-free helper: subscribe holds a write lock
// while calling it, so allocation pressure matters more than the
// 16 lines saved.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
