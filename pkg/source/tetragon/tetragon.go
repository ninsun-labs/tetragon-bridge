// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

// Package tetragon adapts the upstream Tetragon FineGuidanceSensors
// GetEvents stream into the bridge Hub. The adapter is owned by the
// bridge process; it dials Tetragon's UDS (one connection per node),
// consumes the firehose, filters down to the kinds we care about,
// and translates each event into the matching bridge proto type.
//
// The translation table:
//   - Tetragon ProcessExec → bridge ProcessExec
//   - Tetragon ProcessKprobe + functionName ∈ {udp_sendmsg,
//     tcp_sendmsg, ...} → bridge DNSQuery (kprobe DNS surface)
//   - Tetragon ProcessKprobe + functionName == "fd_install" or
//     "security_file_open" → bridge FileOpen
//
// The adapter is conservative: any kprobe shape it doesn't recognise
// is dropped silently. A counter
// ugallu_tetragon_bridge_unknown_kprobe_total exposes the rate so
// ops can spot a Tetragon upgrade that introduced a new shape.
package tetragon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	tetragonv1 "github.com/cilium/tetragon/api/v1/tetragon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ninsun-labs/tetragon-bridge/pkg/server"
)

// DefaultUnixSocket is the conventional Tetragon UDS path.
const DefaultUnixSocket = "/var/run/tetragon/tetragon.sock"

// DefaultReconnectBase is the starting exponential-backoff interval
// for reconnects.
const DefaultReconnectBase = 2 * time.Second

// Config wires the adapter.
type Config struct {
	// Endpoint is the Tetragon endpoint. UDS form: "unix:///var/...";
	// TCP form: "host:port". Empty falls back to DefaultUnixSocket.
	Endpoint string

	// NodeName stamps every emitted event with the node identifier
	// (usually the kubelet's spec.nodeName). Required.
	NodeName string

	// ReconnectBase is the starting backoff on reconnect; doubles up
	// to 30s. Zero falls back to DefaultReconnectBase.
	ReconnectBase time.Duration
}

// Source is the production source.Source implementation that talks
// to a real Tetragon DaemonSet.
type Source struct {
	cfg Config
}

// New validates cfg and returns a Source ready to Run.
func New(cfg Config) (*Source, error) {
	if cfg.NodeName == "" {
		return nil, errors.New("tetragon source: NodeName is required")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "unix://" + DefaultUnixSocket
	}
	if cfg.ReconnectBase <= 0 {
		cfg.ReconnectBase = DefaultReconnectBase
	}
	return &Source{cfg: cfg}, nil
}

// Name implements source.Source.
func (s *Source) Name() string { return "tetragon" }

// Run dials Tetragon and pumps events until ctx cancellation.
// Reconnects with exponential backoff on transient errors.
func (s *Source) Run(ctx context.Context, hub *server.Hub) error {
	backoff := s.cfg.ReconnectBase
	for {
		err := s.runOnce(ctx, hub)
		if err == nil {
			return nil // ctx done
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if next := backoff * 2; next > 30*time.Second {
			backoff = 30 * time.Second
		} else {
			backoff = next
		}
	}
}

// runOnce performs one dial + consume cycle. Returns nil only when
// ctx is cancelled mid-stream.
func (s *Source) runOnce(ctx context.Context, hub *server.Hub) error {
	conn, err := dial(ctx, s.cfg.Endpoint)
	if err != nil {
		return fmt.Errorf("dial %s: %w", s.cfg.Endpoint, err)
	}
	defer func() { _ = conn.Close() }()

	client := tetragonv1.NewFineGuidanceSensorsClient(conn)
	stream, err := client.GetEvents(ctx, &tetragonv1.GetEventsRequest{})
	if err != nil {
		return fmt.Errorf("GetEvents: %w", err)
	}
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return errors.New("tetragon stream ended (EOF)")
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}
		s.dispatch(hub, resp)
	}
}

// dial picks UDS vs TCP based on the endpoint scheme.
func dial(_ context.Context, endpoint string) (*grpc.ClientConn, error) {
	if strings.HasPrefix(endpoint, "unix://") {
		path := strings.TrimPrefix(endpoint, "unix://")
		return grpc.NewClient(
			"unix:"+path,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
				p := strings.TrimPrefix(addr, "unix:")
				var d net.Dialer
				return d.DialContext(ctx, "unix", p)
			}),
		)
	}
	return grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// dispatch routes a Tetragon GetEventsResponse to the matching
// translator + Hub.Publish*.
func (s *Source) dispatch(hub *server.Hub, resp *tetragonv1.GetEventsResponse) {
	switch ev := resp.GetEvent().(type) {
	case *tetragonv1.GetEventsResponse_ProcessExec:
		if px := translateProcessExec(s.cfg.NodeName, ev.ProcessExec); px != nil {
			hub.PublishProcessExec(px)
		}
	case *tetragonv1.GetEventsResponse_ProcessKprobe:
		switch classifyKprobe(ev.ProcessKprobe.GetFunctionName()) {
		case kprobeKindDNS:
			if dq := translateDNSQuery(s.cfg.NodeName, ev.ProcessKprobe); dq != nil {
				hub.PublishDNSQuery(dq)
			}
		case kprobeKindFileOpen:
			if fo := translateFileOpen(s.cfg.NodeName, ev.ProcessKprobe); fo != nil {
				hub.PublishFileOpen(fo)
			}
		default:
			// Unknown kprobe shape - dropped silently. Counter
			// surfaces the rate (metrics package).
		}
	}
}
