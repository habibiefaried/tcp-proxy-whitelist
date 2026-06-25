//go:build !linux

package main

import "net"

// setQuickAck is a no-op on non-Linux platforms.
func setQuickAck(conn *net.TCPConn) {}

// relay delegates to the buffer-pool-based copier on non-Linux platforms.
func relay(a, b *net.TCPConn) {
	bufferRelay(a, b)
}

// tuneListener returns a listener without SO_REUSEPORT (not portable).
func tuneListener(network, address string) (net.Listener, error) {
	return net.Listen(network, address)
}
