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
func handleConnection(client net.Conn, remoteAddr string, whitelist []*net.IPNet, dialTimeout time.Duration) {
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

	log.Printf("proxying %s <-> %s", remoteIP, remoteAddr)
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
// It is used on non-Linux platforms and as a fallback when splice(2) is unavailable.
func bufferRelay(a, b *net.TCPConn) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := bufPool.Get().([]byte)
		defer bufPool.Put(buf)
		io.CopyBuffer(a, b, buf)
		a.CloseWrite()
	}()

	go func() {
		defer wg.Done()
		buf := bufPool.Get().([]byte)
		defer bufPool.Put(buf)
		io.CopyBuffer(b, a, buf)
		b.CloseWrite()
	}()

	wg.Wait()
}
