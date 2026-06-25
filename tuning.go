package main

import (
	"net"
	"time"
)

// tuneConn applies TCP options to reduce latency and improve throughput.
//   - TCP_NODELAY disables Nagle's algorithm (eliminates 200ms coalescing delay).
//   - SO_KEEPALIVE detects silently-dead connections.
//   - Enlarged read/write buffers reduce userspace/kernel context switches.
func tuneConn(conn *net.TCPConn) {
	conn.SetNoDelay(true)
	conn.SetKeepAlive(true)
	conn.SetKeepAlivePeriod(30 * time.Second)
	conn.SetReadBuffer(256 * 1024)
	conn.SetWriteBuffer(256 * 1024)

	setQuickAck(conn) // platform-specific (Linux TCP_QUICKACK)
}
