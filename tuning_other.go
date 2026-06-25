//go:build !linux

package main

import (
	"net"
	"sync"
	"time"
)

// setQuickAck is a no-op on non-Linux platforms.
func setQuickAck(conn *net.TCPConn) {}

// relay performs bidirectional copy with idle timeout.
func relay(a, b *net.TCPConn, idleTimeout time.Duration) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyWithIdleTimeout(a, b, idleTimeout) }()
	go func() { defer wg.Done(); copyWithIdleTimeout(b, a, idleTimeout) }()
	wg.Wait()
}

// tuneListener returns a listener without SO_REUSEPORT (not portable).
func tuneListener(network, address string) (net.Listener, error) {
	return net.Listen(network, address)
}
