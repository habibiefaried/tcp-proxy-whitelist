//go:build linux

package main

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// setQuickAck enables TCP_QUICKACK so the kernel ACKs data immediately instead of
// waiting for a delayed-ACK timer (which can add up to 40ms per direction).
func setQuickAck(conn *net.TCPConn) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	raw.Control(func(fd uintptr) {
		unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_QUICKACK, 1)
	})
}

// relay performs bidirectional copy using bufferRelay.
// Go's net.TCPConn.ReadFrom automatically uses splice(2) on Linux when both
// endpoints are TCP sockets, giving us zero-copy without managing raw FDs.
// This integrates with Go's runtime poller so SetDeadline works correctly.
func relay(a, b *net.TCPConn) {
	bufferRelay(a, b)
}

// tuneListener creates a TCP listener with SO_REUSEPORT enabled so multiple
// processes can bind the same port for linear multi-core scaling.
func tuneListener(network, address string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			c.Control(func(fd uintptr) {
				opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			})
			return opErr
		},
	}
	return lc.Listen(context.Background(), network, address)
}
