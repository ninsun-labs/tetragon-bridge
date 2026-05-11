// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	bridgev1 "github.com/ninsun-labs/tetragon-bridge/proto/v1"
)

// DefaultDefaultMaxEventsPerSec is the egress cap applied when a
// subscriber sends max_events_per_sec=0. Two-thirds of the default
// per-subscriber buffer per second so a healthy subscriber drains
// the ring within ~15s of a burst.
const DefaultDefaultMaxEventsPerSec = 700

// ServerConfig configures the gRPC service.
type ServerConfig struct {
	// BearerToken is the shared secret subscribers must present in
	// the "authorization: Bearer <token>" metadata header. Empty =
	// auth disabled (lab/dev only - never in cluster mTLS-off mode).
	BearerToken string

	// PerSubscriberBuffer overrides the Hub ring depth. Zero falls
	// back to DefaultPerSubscriberBuffer.
	PerSubscriberBuffer int

	// DefaultMaxEventsPerSec is the cap applied when the client
	// sends max_events_per_sec=0. Zero falls back to
	// DefaultDefaultMaxEventsPerSec.
	DefaultMaxEventsPerSec uint32
}

// Server implements bridgev1.TetragonBridgeServer. The Hub is exposed
// so the upstream Tetragon adapter can publish into it; it lives on
// the same struct to keep the gRPC server package self-contained.
type Server struct {
	bridgev1.UnimplementedTetragonBridgeServer

	Hub *Hub
	cfg ServerConfig
}

// NewServer returns a Server with the configured Hub. cfg is copied;
// runtime mutation is not supported.
func NewServer(cfg *ServerConfig) *Server {
	if cfg == nil {
		cfg = &ServerConfig{}
	}
	if cfg.DefaultMaxEventsPerSec == 0 {
		cfg.DefaultMaxEventsPerSec = DefaultDefaultMaxEventsPerSec
	}
	return &Server{
		Hub: NewHub(cfg.PerSubscriberBuffer),
		cfg: *cfg,
	}
}

// AuthInterceptor returns a stream-server interceptor that enforces
// the bearer token when configured. Returned nil when BearerToken
// is empty so callers can chain it unconditionally.
func (s *Server) AuthInterceptor() grpc.StreamServerInterceptor {
	if s.cfg.BearerToken == "" {
		return nil
	}
	expected := "Bearer " + s.cfg.BearerToken
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		md, ok := metadata.FromIncomingContext(ss.Context())
		if !ok {
			return status.Error(codes.Unauthenticated, "missing metadata")
		}
		auth := md.Get("authorization")
		if len(auth) == 0 || auth[0] != expected {
			return status.Error(codes.Unauthenticated, "bad authorization")
		}
		return handler(srv, ss)
	}
}

// StreamProcessExec implements bridgev1.TetragonBridgeServer.
func (s *Server) StreamProcessExec(req *bridgev1.SubscribeRequest, stream bridgev1.TetragonBridge_StreamProcessExecServer) error {
	if err := validateSubscribeRequest(req, false); err != nil {
		return err
	}
	ch, cancel := s.Hub.SubscribeProcessExec(stream.Context(), req.GetSubscriberId())
	defer cancel()
	limiter := newLimiter(req.GetMaxEventsPerSec(), s.cfg.DefaultMaxEventsPerSec)
	return pump(stream.Context(), ch, limiter, func(ev *bridgev1.ProcessExec) error {
		if !matchesPodFilter(ev.GetProcess().GetPod(), req.GetTargetPod()) {
			return nil
		}
		return stream.Send(ev)
	})
}

// StreamDNSQuery implements bridgev1.TetragonBridgeServer.
func (s *Server) StreamDNSQuery(req *bridgev1.SubscribeRequest, stream bridgev1.TetragonBridge_StreamDNSQueryServer) error {
	if err := validateSubscribeRequest(req, false); err != nil {
		return err
	}
	ch, cancel := s.Hub.SubscribeDNSQuery(stream.Context(), req.GetSubscriberId())
	defer cancel()
	limiter := newLimiter(req.GetMaxEventsPerSec(), s.cfg.DefaultMaxEventsPerSec)
	return pump(stream.Context(), ch, limiter, func(ev *bridgev1.DNSQuery) error {
		if !matchesPodFilter(ev.GetProcess().GetPod(), req.GetTargetPod()) {
			return nil
		}
		return stream.Send(ev)
	})
}

// StreamFileOpen implements bridgev1.TetragonBridgeServer. Unlike the
// other two streams, FileOpen requires target_pod - an unfiltered
// open() firehose would saturate the bridge on a busy node.
func (s *Server) StreamFileOpen(req *bridgev1.SubscribeRequest, stream bridgev1.TetragonBridge_StreamFileOpenServer) error {
	if err := validateSubscribeRequest(req, true); err != nil {
		return err
	}
	ch, cancel := s.Hub.SubscribeFileOpen(stream.Context(), req.GetSubscriberId())
	defer cancel()
	limiter := newLimiter(req.GetMaxEventsPerSec(), s.cfg.DefaultMaxEventsPerSec)
	return pump(stream.Context(), ch, limiter, func(ev *bridgev1.FileOpen) error {
		if !matchesPodFilter(ev.GetProcess().GetPod(), req.GetTargetPod()) {
			return nil
		}
		return stream.Send(ev)
	})
}

// validateSubscribeRequest performs the common request guards. The
// requirePod flag toggles the StreamFileOpen-only target_pod
// requirement.
func validateSubscribeRequest(req *bridgev1.SubscribeRequest, requirePod bool) error {
	if req == nil || strings.TrimSpace(req.GetSubscriberId()) == "" {
		return status.Error(codes.InvalidArgument, "subscriber_id is required")
	}
	if requirePod {
		p := req.GetTargetPod()
		if p == nil || p.GetNamespace() == "" || p.GetName() == "" {
			return status.Error(codes.InvalidArgument, "target_pod with namespace+name is required for this stream")
		}
	}
	return nil
}

// matchesPodFilter returns true when the event's Pod matches the
// optional target_pod filter on the request. A nil/zero filter matches
// every event.
func matchesPodFilter(podOnEvent, filter *bridgev1.PodRef) bool {
	if filter == nil || filter.GetNamespace() == "" || filter.GetName() == "" {
		return true
	}
	if podOnEvent == nil {
		return false
	}
	return podOnEvent.GetNamespace() == filter.GetNamespace() && podOnEvent.GetName() == filter.GetName()
}

// pump is the shared per-stream loop: drain the hub channel through
// the rate limiter and deliver via send. Returns nil on ctx done,
// gRPC error otherwise.
func pump[T any](ctx context.Context, ch <-chan T, limiter *rate.Limiter, send func(T) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := limiter.Wait(ctx); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				return status.Error(codes.ResourceExhausted, err.Error())
			}
			if err := send(ev); err != nil {
				return err
			}
		}
	}
}

// newLimiter builds a token-bucket from the requested rate. A zero
// requested rate falls back to the server default; otherwise the
// stricter of (requested, server-default) wins so a misbehaving
// client cannot raise its own cap.
func newLimiter(reqPerSec, defaultPerSec uint32) *rate.Limiter {
	r := reqPerSec
	if r == 0 || (defaultPerSec > 0 && r > defaultPerSec) {
		r = defaultPerSec
	}
	if r == 0 {
		// Both unset - apply the type-default rather than 0 (which
		// would block forever). This branch should be unreachable
		// because NewServer applies DefaultDefaultMaxEventsPerSec.
		r = DefaultDefaultMaxEventsPerSec
	}
	// Burst = 1 second of nominal rate so subscribers absorb tiny
	// schedule jitter without ResourceExhausted churn.
	limit := rate.Every(time.Second / time.Duration(r))
	return rate.NewLimiter(limit, int(r))
}
