package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Unit: isWhitelisted
// ---------------------------------------------------------------------------

func TestIsWhitelisted(t *testing.T) {
	parse := func(s string) *net.IPNet {
		_, cidr, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("bad test CIDR %q: %v", s, err)
		}
		return cidr
	}

	tests := []struct {
		name      string
		ip        string
		whitelist []*net.IPNet
		want      bool
	}{
		{
			name:      "empty whitelist blocks everything",
			ip:        "127.0.0.1",
			whitelist: nil,
			want:      false,
		},
		{
			name:      "single CIDR match",
			ip:        "10.0.0.5",
			whitelist: []*net.IPNet{parse("10.0.0.0/8")},
			want:      true,
		},
		{
			name:      "single CIDR no match",
			ip:        "192.168.1.1",
			whitelist: []*net.IPNet{parse("10.0.0.0/8")},
			want:      false,
		},
		{
			name:      "multiple CIDRs match second",
			ip:        "172.16.0.5",
			whitelist: []*net.IPNet{parse("10.0.0.0/8"), parse("172.16.0.0/12")},
			want:      true,
		},
		{
			name:      "allow all (0.0.0.0/0)",
			ip:        "8.8.8.8",
			whitelist: []*net.IPNet{parse("0.0.0.0/0")},
			want:      true,
		},
		{
			name:      "/32 exact match",
			ip:        "192.168.1.42",
			whitelist: []*net.IPNet{parse("192.168.1.42/32")},
			want:      true,
		},
		{
			name:      "/32 no match off by one",
			ip:        "192.168.1.43",
			whitelist: []*net.IPNet{parse("192.168.1.42/32")},
			want:      false,
		},
		{
			name:      "IPv6 localhost match",
			ip:        "::1",
			whitelist: []*net.IPNet{parse("::1/128")},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWhitelisted(net.ParseIP(tt.ip), tt.whitelist)
			if got != tt.want {
				t.Errorf("isWhitelisted(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit: bufPool
// ---------------------------------------------------------------------------

func TestBufPool(t *testing.T) {
	buf := bufPool.Get().([]byte)
	if len(buf) != defaultCopyBufSize {
		t.Errorf("buffer size = %d, want %d", len(buf), defaultCopyBufSize)
	}
	bufPool.Put(buf)

	// Second get should return the same buffer (or same-sized buffer).
	buf2 := bufPool.Get().([]byte)
	if len(buf2) != defaultCopyBufSize {
		t.Errorf("second buffer size = %d, want %d", len(buf2), defaultCopyBufSize)
	}
	bufPool.Put(buf2)
}

// ---------------------------------------------------------------------------
// Integration helpers
// ---------------------------------------------------------------------------

// echoServer starts a TCP server that echoes back whatever it receives.
// It returns the listening address and a stop function.
func echoServer(tb testing.TB) (addr string, stop func()) {
	tb.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("echo server listen: %v", err)
	}
	addr = l.Addr().String()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return // listener closed
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn) // echo
			}()
		}
	}()

	return addr, func() {
		l.Close()
		wg.Wait()
	}
}

// startProxy starts a proxy listener with the given config and returns the
// listening address and a stop function.
func startProxy(tb testing.TB, cfg *config) (addr string, stop func()) {
	tb.Helper()

	l, err := tuneListener("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("proxy listen: %v", err)
	}
	addr = l.Addr().String()

	_, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				handleConnection(conn, cfg.remoteAddr, cfg.whitelist, cfg.dialTimeout)
			}()
		}
	}()

	return addr, func() {
		cancel()
		l.Close()
		wg.Wait()
	}
}

// proxyTestConfig creates a config pointing at the given upstream.
func proxyTestConfig(upstreamAddr string, whitelist ...string) *config {
	var nets []*net.IPNet
	for _, s := range whitelist {
		_, cidr, err := net.ParseCIDR(s)
		if err != nil {
			panic("bad test CIDR: " + s)
		}
		nets = append(nets, cidr)
	}
	return &config{
		remoteAddr:  upstreamAddr,
		whitelist:   nets,
		dialTimeout: 2 * time.Second,
	}
}

// sendRecv connects to addr, sends msg, and reads back the echoed response.
func sendRecv(tb testing.TB, addr, msg string) string {
	tb.Helper()

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		tb.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, msg); err != nil {
		tb.Fatalf("write: %v", err)
	}

	// Close write side so the echo server knows we're done.
	if tcp, ok := conn.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}

	reply, err := io.ReadAll(conn)
	if err != nil {
		tb.Fatalf("read: %v", err)
	}
	return string(reply)
}

// ---------------------------------------------------------------------------
// Integration: proxy round-trip
// ---------------------------------------------------------------------------

func TestProxyRoundTrip(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	// Whitelist loopback so our test client is allowed.
	cfg := proxyTestConfig(upstreamAddr, "127.0.0.0/8")
	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	reply := sendRecv(t, proxyAddr, "hello, proxy!")
	if reply != "hello, proxy!" {
		t.Errorf("echo = %q, want %q", reply, "hello, proxy!")
	}
}

func TestProxyLargePayload(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	cfg := proxyTestConfig(upstreamAddr, "127.0.0.0/8")
	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	// 1 MiB payload — exercises the full buffer/splice path.
	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}

	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(reply) != len(payload) {
		t.Errorf("reply length = %d, want %d", len(reply), len(payload))
	}
}

// ---------------------------------------------------------------------------
// Integration: rejection
// ---------------------------------------------------------------------------

func TestProxyRejection(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	// Whitelist only 10.0.0.0/8 — 127.0.0.1 is not in it.
	cfg := proxyTestConfig(upstreamAddr, "10.0.0.0/8")
	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// The proxy accepts then immediately closes.  Read should return 0 bytes.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	n, _ := conn.Read(make([]byte, 64))
	conn.Close()

	if n != 0 {
		t.Errorf("read %d bytes from rejected connection, want 0", n)
	}
}

func TestProxyEmptyWhitelistRejectAll(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	// No whitelist entries — all connections blocked.
	cfg := proxyTestConfig(upstreamAddr) // empty whitelist
	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	n, _ := conn.Read(make([]byte, 64))
	conn.Close()

	if n != 0 {
		t.Errorf("read %d bytes, want 0 (all connections should be blocked)", n)
	}
}

func TestProxyAllowAll(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	// 0.0.0.0/0 matches everything.
	cfg := proxyTestConfig(upstreamAddr, "0.0.0.0/0")
	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	reply := sendRecv(t, proxyAddr, "anyone can connect")
	if reply != "anyone can connect" {
		t.Errorf("echo = %q, want %q", reply, "anyone can connect")
	}
}

// ---------------------------------------------------------------------------
// Integration: concurrent connections
// ---------------------------------------------------------------------------

func TestProxyConcurrentConnections(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	cfg := proxyTestConfig(upstreamAddr, "127.0.0.0/8")
	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	const numConns = 50
	var wg sync.WaitGroup
	errCh := make(chan error, numConns)

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			msg := fmt.Sprintf("ping-%d", id)

			conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
			if err != nil {
				errCh <- fmt.Errorf("goroutine %d dial: %w", id, err)
				return
			}
			defer conn.Close()

			if _, err := io.WriteString(conn, msg); err != nil {
				errCh <- fmt.Errorf("goroutine %d write: %w", id, err)
				return
			}

			// Close write side so the echo server knows we're done.
			if tcp, ok := conn.(*net.TCPConn); ok {
				tcp.CloseWrite()
			}

			reply, err := io.ReadAll(conn)
			if err != nil {
				errCh <- fmt.Errorf("goroutine %d read: %w", id, err)
				return
			}
			if string(reply) != msg {
				errCh <- fmt.Errorf("goroutine %d: echo = %q, want %q", id, string(reply), msg)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

// ---------------------------------------------------------------------------
// Integration: graceful shutdown
// ---------------------------------------------------------------------------

func TestGracefulShutdown(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	cfg := proxyTestConfig(upstreamAddr, "127.0.0.0/8")
	proxyAddr, proxyStop := startProxy(t, cfg)

	// Send a message to verify proxy is up.
	reply := sendRecv(t, proxyAddr, "before-shutdown")
	if reply != "before-shutdown" {
		t.Fatalf("pre-shutdown echo = %q", reply)
	}

	// Stop the proxy and verify it shuts down cleanly.
	proxyStop()

	// Connecting after shutdown should fail.
	_, err := net.DialTimeout("tcp", proxyAddr, 500*time.Millisecond)
	if err == nil {
		t.Error("expected connection to be refused after shutdown")
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// BenchmarkIsWhitelisted measures the whitelist check for a typical setup.
func BenchmarkIsWhitelisted(b *testing.B) {
	parse := func(s string) *net.IPNet {
		_, cidr, _ := net.ParseCIDR(s)
		return cidr
	}
	whitelist := []*net.IPNet{
		parse("10.0.0.0/8"),
		parse("172.16.0.0/12"),
		parse("192.168.0.0/16"),
	}
	ip := net.ParseIP("172.16.5.100")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		isWhitelisted(ip, whitelist)
	}
}

// BenchmarkBufPool measures pool get/put overhead.
func BenchmarkBufPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buf := bufPool.Get().([]byte)
		bufPool.Put(buf)
	}
}

// BenchmarkProxyThroughput measures end-to-end throughput through the proxy.
func BenchmarkProxyThroughput(b *testing.B) {
	// Start echo server on a random port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	upstreamAddr := l.Addr().String()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()

	// Start proxy.
	cfg := proxyTestConfig(upstreamAddr, "127.0.0.0/8")
	proxyAddr, proxyStop := startProxy(b, cfg)
	defer proxyStop()

	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	b.ResetTimer()
	b.SetBytes(int64(len(payload) * 2)) // sent + echoed

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
			if err != nil {
				b.Fatal(err)
			}
			conn.Write(payload)
			if tcp, ok := conn.(*net.TCPConn); ok {
				tcp.CloseWrite()
			}
			io.ReadAll(conn)
			conn.Close()
		}
	})
}
