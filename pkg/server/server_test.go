// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	bridgev1 "github.com/ninsun-labs/tetragon-bridge/proto/v1"
)

// startServer wires Server in a bufconn listener and returns a
// connected client + the underlying server for direct Hub access.
func startServer(t *testing.T, cfg *ServerConfig) (bridgev1.TetragonBridgeClient, *Server, *grpc.Server) {
	t.Helper()
	lis := bufconn.Listen(1 << 16)
	srv := NewServer(cfg)
	opts := []grpc.ServerOption{}
	if interceptor := srv.AuthInterceptor(); interceptor != nil {
		opts = append(opts, grpc.StreamInterceptor(interceptor))
	}
	gs := grpc.NewServer(opts...)
	bridgev1.RegisterTetragonBridgeServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { gs.Stop() })

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridgev1.NewTetragonBridgeClient(conn), srv, gs
}

// TestStreamProcessExec_DeliversEvents covers the happy path:
// Subscribe → publish → receive on the client side.
func TestStreamProcessExec_DeliversEvents(t *testing.T) {
	client, srv, _ := startServer(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := client.StreamProcessExec(ctx, &bridgev1.SubscribeRequest{SubscriberId: "test"})
	if err != nil {
		t.Fatalf("StreamProcessExec: %v", err)
	}

	// Give the subscribe call time to attach to the hub before the
	// publish (otherwise the publish races ahead and is dropped).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(srv.Hub.Snapshot()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv.Hub.PublishProcessExec(&bridgev1.ProcessExec{NodeName: "n1"})

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.GetNodeName() != "n1" {
		t.Errorf("NodeName = %q", ev.GetNodeName())
	}
}

// TestStreamFileOpen_RequiresTargetPod asserts the FileOpen-only
// guard returns InvalidArgument when target_pod is missing.
func TestStreamFileOpen_RequiresTargetPod(t *testing.T) {
	client, _, _ := startServer(t, nil)
	stream, err := client.StreamFileOpen(context.Background(), &bridgev1.SubscribeRequest{SubscriberId: "x"})
	if err != nil {
		t.Fatalf("StreamFileOpen: %v", err)
	}
	_, err = stream.Recv()
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("err = %v; want InvalidArgument", err)
	}
}

// TestAuthInterceptor_RejectsMissingToken proves the bearer-token
// gate kicks in when configured.
func TestAuthInterceptor_RejectsMissingToken(t *testing.T) {
	client, _, _ := startServer(t, &ServerConfig{BearerToken: "secret"})
	stream, err := client.StreamProcessExec(context.Background(), &bridgev1.SubscribeRequest{SubscriberId: "x"})
	if err != nil {
		t.Fatalf("StreamProcessExec: %v", err)
	}
	_, err = stream.Recv()
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("err = %v; want Unauthenticated", err)
	}
}

// TestAuthInterceptor_AcceptsValidToken pairs with the negative test.
func TestAuthInterceptor_AcceptsValidToken(t *testing.T) {
	client, srv, _ := startServer(t, &ServerConfig{BearerToken: "secret"})
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer secret")
	stream, err := client.StreamProcessExec(ctx, &bridgev1.SubscribeRequest{SubscriberId: "x"})
	if err != nil {
		t.Fatalf("StreamProcessExec: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(srv.Hub.Snapshot()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv.Hub.PublishProcessExec(&bridgev1.ProcessExec{NodeName: "n1"})
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv: %v", err)
	}
}

// TestStreamProcessExec_TargetPodFilter verifies events not matching
// the requested Pod are filtered out before reaching the wire.
func TestStreamProcessExec_TargetPodFilter(t *testing.T) {
	client, srv, _ := startServer(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.StreamProcessExec(ctx, &bridgev1.SubscribeRequest{
		SubscriberId: "filter",
		TargetPod:    &bridgev1.PodRef{Namespace: "team-a", Name: "want"},
	})
	if err != nil {
		t.Fatalf("StreamProcessExec: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(srv.Hub.Snapshot()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Publish one matching + two non-matching; expect to see only
	// the matching one.
	srv.Hub.PublishProcessExec(&bridgev1.ProcessExec{NodeName: "drop-1", Process: &bridgev1.ProcessInfo{Pod: &bridgev1.PodRef{Namespace: "team-a", Name: "other"}}})
	srv.Hub.PublishProcessExec(&bridgev1.ProcessExec{NodeName: "keep", Process: &bridgev1.ProcessInfo{Pod: &bridgev1.PodRef{Namespace: "team-a", Name: "want"}}})
	srv.Hub.PublishProcessExec(&bridgev1.ProcessExec{NodeName: "drop-2"})

	rcvCtx, rcvCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rcvCancel()
	type rcv struct {
		ev  *bridgev1.ProcessExec
		err error
	}
	out := make(chan rcv, 1)
	go func() {
		ev, err := stream.Recv()
		out <- rcv{ev, err}
	}()
	select {
	case r := <-out:
		if r.err != nil {
			t.Fatalf("Recv: %v", r.err)
		}
		if r.ev.GetNodeName() != "keep" {
			t.Errorf("NodeName = %q (filter let a non-match through)", r.ev.GetNodeName())
		}
	case <-rcvCtx.Done():
		t.Fatal("Recv timed out - filter dropped the matching event")
	}
}
