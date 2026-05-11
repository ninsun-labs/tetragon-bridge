// Copyright 2026 The ninsun-labs Authors.
// SPDX-License-Identifier: Apache-2.0

package tetragon

import (
	"net"
	"strings"

	tetragonv1 "github.com/cilium/tetragon/api/v1/tetragon"
	"google.golang.org/protobuf/types/known/timestamppb"

	bridgev1 "github.com/ninsun-labs/tetragon-bridge/proto/v1"
)

// kprobeKind discriminates which bridge stream a Tetragon kprobe
// translates to.
type kprobeKind int

const (
	kprobeKindUnknown kprobeKind = iota
	kprobeKindDNS
	kprobeKindFileOpen
)

// classifyKprobe maps the kernel function name Tetragon hooked into
// the matching kprobeKind. The set is intentionally narrow - anything
// outside it is dropped (and counted) so a future Tetragon shape
// doesn't quietly leak into the wrong stream.
func classifyKprobe(fn string) kprobeKind {
	switch fn {
	// DNS surface: Tetragon CRDs that hook udp_sendmsg / tcp_sendmsg
	// + filter on dst_port == 53 emit kprobes that carry the dst
	// sockaddr in args[0].
	case "udp_sendmsg", "tcp_sendmsg":
		return kprobeKindDNS
	// Open() surface: Tetragon CRDs hook one of these per kernel
	// version. fd_install captures the post-LSM result; security_
	// file_open captures pre-LSM.
	case "fd_install", "security_file_open":
		return kprobeKindFileOpen
	default:
		return kprobeKindUnknown
	}
}

// translateProcess maps the Tetragon Process bundle into the bridge's
// ProcessInfo (the resolver-style attribution every typed event
// carries). Returns nil when src is nil.
func translateProcess(src *tetragonv1.Process) *bridgev1.ProcessInfo {
	if src == nil {
		return nil
	}
	out := &bridgev1.ProcessInfo{
		Pid:        src.GetPid().GetValue(),
		BinaryPath: src.GetBinary(),
	}
	if c := src.GetPod().GetContainer(); c != nil {
		out.ContainerId = c.GetId()
	}
	if pod := src.GetPod(); pod != nil && pod.GetName() != "" {
		out.Pod = &bridgev1.PodRef{Namespace: pod.GetNamespace(), Name: pod.GetName()}
	}
	return out
}

// translateProcessExec turns a Tetragon ProcessExec into a bridge
// ProcessExec. Argv splits Tetragon's space-joined Arguments by the
// same separator Tetragon uses internally (best-effort - the upstream
// API doesn't expose the original argv vector).
func translateProcessExec(nodeName string, src *tetragonv1.ProcessExec) *bridgev1.ProcessExec {
	if src == nil || src.GetProcess() == nil {
		return nil
	}
	p := src.GetProcess()
	out := &bridgev1.ProcessExec{
		Timestamp: src.GetProcess().GetStartTime(),
		NodeName:  nodeName,
		Process:   translateProcess(p),
		ParentPid: src.GetParent().GetPid().GetValue(),
		Argv:      splitArgv(p.GetArguments()),
		Uid:       p.GetUid().GetValue(),
	}
	if out.Timestamp == nil {
		out.Timestamp = timestamppb.Now()
	}
	return out
}

// translateDNSQuery extracts (qname, qtype, dst) from a Tetragon
// kprobe args slice. The expected arg shape (Tetragon CRD must
// declare it):
//
//	args[0] = sockaddr_arg (dst IP+port)
//	args[1] = bytes_arg or string_arg with the DNS payload
//
// Anything else is best-effort. Returning nil drops the event.
func translateDNSQuery(nodeName string, src *tetragonv1.ProcessKprobe) *bridgev1.DNSQuery {
	if src == nil || src.GetProcess() == nil {
		return nil
	}
	out := &bridgev1.DNSQuery{
		Timestamp: timestamppb.Now(),
		NodeName:  nodeName,
		Process:   translateProcess(src.GetProcess()),
	}
	for _, arg := range src.GetArgs() {
		switch v := arg.GetArg().(type) {
		case *tetragonv1.KprobeArgument_SockArg:
			sa := v.SockArg
			if sa == nil {
				continue
			}
			if ip := net.ParseIP(sa.GetDaddr()); ip != nil {
				out.DstIp = ip
			}
			out.DstPort = sa.GetDport()
		case *tetragonv1.KprobeArgument_StringArg:
			if out.Qname == "" {
				out.Qname = strings.ToLower(strings.TrimSpace(v.StringArg))
			}
		}
	}
	if out.Qname == "" || out.DstPort == 0 {
		return nil
	}
	if out.Qtype == "" {
		out.Qtype = "A" // best-effort default; the kprobe payload doesn't carry the qtype
	}
	return out
}

// translateFileOpen extracts (path, flags, mode) from a fd_install /
// security_file_open kprobe. Path is required - events without it
// are dropped (the kprobe can't have meaningfully fired).
func translateFileOpen(nodeName string, src *tetragonv1.ProcessKprobe) *bridgev1.FileOpen {
	if src == nil || src.GetProcess() == nil {
		return nil
	}
	out := &bridgev1.FileOpen{
		Timestamp:  timestamppb.Now(),
		NodeName:   nodeName,
		Process:    translateProcess(src.GetProcess()),
		Returncode: src.GetReturn().GetIntArg(),
	}
	for _, arg := range src.GetArgs() {
		switch v := arg.GetArg().(type) {
		case *tetragonv1.KprobeArgument_FileArg:
			if v.FileArg != nil && out.Path == "" {
				out.Path = v.FileArg.GetPath()
				// Tetragon serialises flags as a "|"-joined string
				// (e.g. "O_RDONLY|O_CLOEXEC"). The bridge stays on
				// numeric flags for now - leaving 0 here is honest;
				// a follow-up can parse the string symbol set.
			}
		case *tetragonv1.KprobeArgument_PathArg:
			if v.PathArg != nil && out.Path == "" {
				out.Path = v.PathArg.GetPath()
			}
		case *tetragonv1.KprobeArgument_StringArg:
			if out.Path == "" {
				out.Path = v.StringArg
			}
		case *tetragonv1.KprobeArgument_IntArg:
			if out.Flags == 0 {
				out.Flags = uint32(v.IntArg) //nolint:gosec // sentinel: open() flags bitmap fits a uint32
			} else if out.Mode == 0 {
				out.Mode = uint32(v.IntArg) //nolint:gosec // open() mode bitmap fits a uint32
			}
		}
	}
	if out.Path == "" {
		return nil
	}
	return out
}

// splitArgv tokenizes Tetragon's joined Arguments string. Tetragon
// joins on a single space; argv values containing spaces are
// quote-escaped at the source. We do the inverse: split on
// unquoted spaces.
func splitArgv(args string) []string {
	if args == "" {
		return nil
	}
	out := make([]string, 0, 4)
	var (
		cur     strings.Builder
		inQuote bool
	)
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
