//go:build linux

package main

import (
	"context"
	"net"
	"sync"
	"syscall"
	"time"

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

// relay performs bidirectional copy with idle timeout.
// On Linux, Go's runtime uses splice(2) internally for net.TCPConn I/O
// when possible, giving zero-copy while respecting deadlines.
func relay(a, b *net.TCPConn, idleTimeout time.Duration) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyWithIdleTimeout(a, b, idleTimeout) }()
	go func() { defer wg.Done(); copyWithIdleTimeout(b, a, idleTimeout) }()
	wg.Wait()
}

// tuneListener creates a TCP listener with SO_REUSEPORT and TCP_DEFER_ACCEPT.
// SO_REUSEPORT lets multiple processes bind the same port for multi-core scaling.
// TCP_DEFER_ACCEPT delays accept notification until data arrives, reducing
// wakeups from connections that connect but never send (SYN floods, health checks).
func tuneListener(network, address string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			c.Control(func(fd uintptr) {
				unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
				unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_DEFER_ACCEPT, 1)
				opErr = nil
			})
			return opErr
		},
	}
	return lc.Listen(context.Background(), network, address)
}
