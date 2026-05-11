// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

package tetragon

import (
	"net"
	"testing"

	tetragonv1 "github.com/cilium/tetragon/api/v1/tetragon"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// TestClassifyKprobe pins the small whitelist that drives the
// stream-routing decision.
func TestClassifyKprobe(t *testing.T) {
	cases := map[string]kprobeKind{
		"udp_sendmsg":        kprobeKindDNS,
		"tcp_sendmsg":        kprobeKindDNS,
		"fd_install":         kprobeKindFileOpen,
		"security_file_open": kprobeKindFileOpen,
		"do_sys_openat2":     kprobeKindUnknown,
		"":                   kprobeKindUnknown,
	}
	for fn, want := range cases {
		if got := classifyKprobe(fn); got != want {
			t.Errorf("classifyKprobe(%q) = %v, want %v", fn, got, want)
		}
	}
}

// TestTranslateProcess covers the resolver-bundle mapping including
// the Pod / Container path.
func TestTranslateProcess(t *testing.T) {
	src := &tetragonv1.Process{
		Pid:    wrapperspb.UInt32(4242),
		Binary: "/usr/bin/cat",
		Pod: &tetragonv1.Pod{
			Namespace: "team-a",
			Name:      "client",
			Container: &tetragonv1.Container{Id: "containerd://abc"},
		},
	}
	got := translateProcess(src)
	if got == nil {
		t.Fatal("translateProcess returned nil")
	}
	if got.GetPid() != 4242 {
		t.Errorf("Pid = %d", got.GetPid())
	}
	if got.GetBinaryPath() != "/usr/bin/cat" {
		t.Errorf("BinaryPath = %q", got.GetBinaryPath())
	}
	if got.GetPod().GetNamespace() != "team-a" || got.GetPod().GetName() != "client" {
		t.Errorf("Pod = %+v", got.GetPod())
	}
	if got.GetContainerId() != "containerd://abc" {
		t.Errorf("ContainerId = %q", got.GetContainerId())
	}
}

// TestTranslateProcessExec_Argv verifies the splitArgv quote-aware
// tokenizer.
func TestTranslateProcessExec_Argv(t *testing.T) {
	src := &tetragonv1.ProcessExec{
		Process: &tetragonv1.Process{
			Pid:       wrapperspb.UInt32(7),
			Binary:    "/bin/sh",
			Arguments: `-c "echo hello world" /tmp/x`,
		},
	}
	got := translateProcessExec("node-1", src)
	if got == nil {
		t.Fatal("translateProcessExec returned nil")
	}
	want := []string{"-c", "echo hello world", "/tmp/x"}
	if len(got.GetArgv()) != len(want) {
		t.Fatalf("Argv = %v, want %v", got.GetArgv(), want)
	}
	for i := range want {
		if got.GetArgv()[i] != want[i] {
			t.Errorf("Argv[%d] = %q, want %q", i, got.GetArgv()[i], want[i])
		}
	}
}

// TestTranslateDNSQuery_FromSockArg covers the kprobe-DNS path that
// pulls (DstIP, DstPort) out of a KprobeSock.
func TestTranslateDNSQuery_FromSockArg(t *testing.T) {
	src := &tetragonv1.ProcessKprobe{
		Process: &tetragonv1.Process{Pid: wrapperspb.UInt32(1), Binary: "/bin/curl"},
		Args: []*tetragonv1.KprobeArgument{
			{Arg: &tetragonv1.KprobeArgument_SockArg{SockArg: &tetragonv1.KprobeSock{Daddr: "10.96.0.10", Dport: 53}}},
			{Arg: &tetragonv1.KprobeArgument_StringArg{StringArg: "Example.COM"}},
		},
	}
	got := translateDNSQuery("node-1", src)
	if got == nil {
		t.Fatal("translateDNSQuery returned nil")
	}
	if got.GetQname() != "example.com" {
		t.Errorf("Qname = %q (lowercase + trim expected)", got.GetQname())
	}
	if got.GetDstPort() != 53 {
		t.Errorf("DstPort = %d", got.GetDstPort())
	}
	if !net.IP(got.GetDstIp()).Equal(net.IPv4(10, 96, 0, 10)) {
		t.Errorf("DstIP = %v", net.IP(got.GetDstIp()))
	}
}

// TestTranslateDNSQuery_DropsIncomplete drops events without a Qname
// or DstPort - those carry no useful signal for the detectors.
func TestTranslateDNSQuery_DropsIncomplete(t *testing.T) {
	src := &tetragonv1.ProcessKprobe{
		Process: &tetragonv1.Process{Pid: wrapperspb.UInt32(1)},
		Args:    []*tetragonv1.KprobeArgument{},
	}
	if got := translateDNSQuery("node-1", src); got != nil {
		t.Errorf("expected nil on empty args, got %+v", got)
	}
}

// TestTranslateFileOpen_RequiresPath drops events without a path.
func TestTranslateFileOpen_RequiresPath(t *testing.T) {
	src := &tetragonv1.ProcessKprobe{
		Process: &tetragonv1.Process{Pid: wrapperspb.UInt32(1)},
		Args:    []*tetragonv1.KprobeArgument{},
	}
	if got := translateFileOpen("node-1", src); got != nil {
		t.Errorf("expected nil on missing path, got %+v", got)
	}
}

// TestTranslateFileOpen_ExtractsFromFileArg covers the happy path
// where Tetragon emits a KprobeFile.
func TestTranslateFileOpen_ExtractsFromFileArg(t *testing.T) {
	src := &tetragonv1.ProcessKprobe{
		Process: &tetragonv1.Process{Pid: wrapperspb.UInt32(1)},
		Args: []*tetragonv1.KprobeArgument{
			{Arg: &tetragonv1.KprobeArgument_FileArg{FileArg: &tetragonv1.KprobeFile{Path: "/etc/passwd"}}},
		},
	}
	got := translateFileOpen("node-1", src)
	if got == nil {
		t.Fatal("translateFileOpen returned nil")
	}
	if got.GetPath() != "/etc/passwd" {
		t.Errorf("Path = %q", got.GetPath())
	}
	if got.GetNodeName() != "node-1" {
		t.Errorf("NodeName = %q", got.GetNodeName())
	}
}
