package main

import (
	"net"
	"time"
)

// tuneConn applies TCP socket options to reduce latency.
//   - TCP_NODELAY disables Nagle's algorithm.
//   - SO_KEEPALIVE detects silently-dead peers.
//   - TCP_QUICKACK (Linux) eliminates delayed-ACK latency.
//
// Socket buffer sizes are left at kernel defaults — Linux auto-tunes
// them based on available memory and connection RTT.
func tuneConn(conn *net.TCPConn) {
	conn.SetNoDelay(true)
	conn.SetKeepAlive(true)
	conn.SetKeepAlivePeriod(30 * time.Second)
	setQuickAck(conn)
}
