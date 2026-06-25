package main

import (
	"log"
	"net"
	"sync"
	"time"
)

// defaultCopyBufSize is the buffer size for the relay read/write loop.
// 64KB balances syscall reduction against per-connection memory usage.
const defaultCopyBufSize = 64 * 1024

// bufPool reuses copy buffers across connections to relieve GC pressure.
var bufPool = sync.Pool{
	New: func() any {
		return make([]byte, defaultCopyBufSize)
	},
}

// handleConnection checks the whitelist and proxies the client connection upstream.
func handleConnection(client net.Conn, remoteAddr string, whitelist []*net.IPNet, dialTimeout, idleTimeout time.Duration) {
	defer client.Close()

	tcpAddr, ok := client.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return
	}
	remoteIP := tcpAddr.IP

	log.Printf("connection from %s", remoteIP)

	if !isWhitelisted(remoteIP, whitelist) {
		log.Printf("rejected: %s", remoteIP)
		return
	}

	upstream, err := dialUpstream(remoteAddr, dialTimeout)
	if err != nil {
		log.Printf("dial upstream %s: %v", remoteAddr, err)
		return
	}
	defer upstream.Close()

	upstreamTCP := upstream.(*net.TCPConn)
	clientTCP := client.(*net.TCPConn)

	tuneConn(upstreamTCP)
	tuneConn(clientTCP)

	log.Printf("proxying %s <-> %s (idle timeout %v)", remoteIP, remoteAddr, idleTimeout)
	relay(clientTCP, upstreamTCP, idleTimeout)
}

// isWhitelisted reports whether ip is contained by any CIDR in the whitelist.
// An empty whitelist always returns false.
func isWhitelisted(ip net.IP, whitelist []*net.IPNet) bool {
	for _, cidr := range whitelist {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// copyWithIdleTimeout copies src to dst, resetting the idle deadline after
// each successful read. This mirrors HAProxy's timeout client/server behavior:
// the timer only fires when the connection is truly idle.
func copyWithIdleTimeout(dst, src *net.TCPConn, idleTimeout time.Duration) {
	buf := bufPool.Get().([]byte)
	defer bufPool.Put(buf)

	for {
		src.SetReadDeadline(time.Now().Add(idleTimeout))
		nr, readErr := src.Read(buf)
		if nr > 0 {
			dst.SetWriteDeadline(time.Now().Add(idleTimeout))
			_, writeErr := dst.Write(buf[:nr])
			if writeErr != nil {
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	dst.CloseWrite()
}
