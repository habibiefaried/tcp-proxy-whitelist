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
func relay(a, b *net.TCPConn, idleTimeout time.Duration) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyWithIdleTimeout(a, b, idleTimeout) }()
	go func() { defer wg.Done(); copyWithIdleTimeout(b, a, idleTimeout) }()
	wg.Wait()
}

// tuneListener creates a TCP listener with SO_REUSEPORT and TCP_FASTOPEN.
// SO_REUSEPORT lets multiple processes bind the same port for multi-core scaling.
// TCP_FASTOPEN allows clients to send data in the SYN packet, saving one RTT.
func tuneListener(network, address string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			c.Control(func(fd uintptr) {
				unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
				unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_FASTOPEN, 256)
			})
			return nil
		},
	}
	return lc.Listen(context.Background(), network, address)
}

// dialUpstream connects to the upstream server with TCP Fast Open when possible.
// TFO sends data in the SYN packet, saving one RTT on the upstream connection.
func dialUpstream(address string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{
		Timeout: timeout,
		Control: func(network, address string, c syscall.RawConn) error {
			c.Control(func(fd uintptr) {
				unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_FASTOPEN_CONNECT, 1)
			})
			return nil
		},
	}
	return d.Dial("tcp", address)
}
