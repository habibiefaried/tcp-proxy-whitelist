package main

import (
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// defaultCopyBufSize is the buffer size used by io.CopyBuffer.
// 256KB reduces read/write syscalls by 8x compared to the default 32KB.
const defaultCopyBufSize = 256 * 1024

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

	upstream, err := net.DialTimeout("tcp", remoteAddr, dialTimeout)
	if err != nil {
		log.Printf("dial upstream %s: %v", remoteAddr, err)
		return
	}
	defer upstream.Close()

	upstreamTCP := upstream.(*net.TCPConn)
	clientTCP := client.(*net.TCPConn)

	tuneConn(upstreamTCP)
	tuneConn(clientTCP)

	// Set a hard deadline on the connection lifetime. If the relay takes
	// longer than idleTimeout, both connections are torn down. This prevents
	// goroutine leaks from protocols that use indefinite keep-alive
	// (e.g. HTTP/1.1, Redis idle, database connection pools).
	deadline := time.Now().Add(idleTimeout)
	clientTCP.SetDeadline(deadline)
	upstreamTCP.SetDeadline(deadline)

	log.Printf("proxying %s <-> %s (timeout %v)", remoteIP, remoteAddr, idleTimeout)
	relay(clientTCP, upstreamTCP)
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

// bufferRelay performs bidirectional copy using pooled heap buffers.
// On Linux, io.CopyBuffer between two TCPConns triggers splice(2) automatically
// via net.TCPConn.ReadFrom, giving zero-copy while respecting SetDeadline.
func bufferRelay(a, b *net.TCPConn) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := bufPool.Get().([]byte)
		io.CopyBuffer(a, b, buf)
		bufPool.Put(buf)
		a.CloseWrite()
	}()

	go func() {
		defer wg.Done()
		buf := bufPool.Get().([]byte)
		io.CopyBuffer(b, a, buf)
		bufPool.Put(buf)
		b.CloseWrite()
	}()

	wg.Wait()
}
