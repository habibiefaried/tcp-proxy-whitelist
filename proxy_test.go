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
				handleConnection(conn, cfg.remoteAddr, cfg.whitelist, cfg.dialTimeout, cfg.idleTimeout)
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
		idleTimeout: 3 * time.Second,
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
// Edge case: isWhitelisted
// ---------------------------------------------------------------------------

func TestIsWhitelistedEdgeCases(t *testing.T) {
	parse := func(s string) *net.IPNet {
		_, cidr, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("bad test CIDR %q: %v", s, err)
		}
		return cidr
	}

	// IPv4-mapped IPv6 addresses should match IPv4 CIDRs.
	t.Run("ipv4 mapped ipv6 matches v4 cidr", func(t *testing.T) {
		ip := net.ParseIP("::ffff:10.0.0.5") // IPv4-mapped IPv6
		whitelist := []*net.IPNet{parse("10.0.0.0/8")}
		if !isWhitelisted(ip, whitelist) {
			t.Error("IPv4-mapped IPv6 should match IPv4 CIDR")
		}
	})

	// CIDR with host bits set (e.g. 10.0.0.5/8) — net.ParseCIDR normalizes
	// this to 10.0.0.0/8 internally, so the match still works.
	t.Run("cidr with host bits set normalizes", func(t *testing.T) {
		ip := net.ParseIP("10.255.255.255")
		whitelist := []*net.IPNet{parse("10.0.0.5/8")}
		if !isWhitelisted(ip, whitelist) {
			t.Error("10.0.0.5/8 should normalize to 10.0.0.0/8")
		}
	})

	// Nil IP should not panic.
	t.Run("nil ip", func(t *testing.T) {
		whitelist := []*net.IPNet{parse("10.0.0.0/8")}
		if isWhitelisted(nil, whitelist) {
			t.Error("nil IP should not match anything")
		}
	})
}

// ---------------------------------------------------------------------------
// Edge case: CIDR whitelist parsing (parseConfig via env vars)
// ---------------------------------------------------------------------------

func TestParseConfigWhitelistEdgeCases(t *testing.T) {
	// Set valid BIND_PORT and REMOTE_ADDR_PAIR so parseConfig doesn't Fatal.
	// t.Setenv scopes the change to this test and restores on cleanup.
	t.Setenv("BIND_PORT", "9090")
	t.Setenv("REMOTE_ADDR_PAIR", "10.0.0.1:8080")

	t.Run("trailing comma", func(t *testing.T) {
		t.Setenv("WHITELISTED_SUBNET", "10.0.0.0/8,")
		cfg := parseConfig()
		if len(cfg.whitelist) != 1 {
			t.Errorf("trailing comma: got %d CIDRs, want 1", len(cfg.whitelist))
		}
	})

	t.Run("leading comma", func(t *testing.T) {
		t.Setenv("WHITELISTED_SUBNET", ",10.0.0.0/8")
		cfg := parseConfig()
		if len(cfg.whitelist) != 1 {
			t.Errorf("leading comma: got %d CIDRs, want 1", len(cfg.whitelist))
		}
	})

	t.Run("only commas and whitespace", func(t *testing.T) {
		t.Setenv("WHITELISTED_SUBNET", "  ,  ,  ")
		cfg := parseConfig()
		if len(cfg.whitelist) != 0 {
			t.Errorf("only commas: got %d CIDRs, want 0 (block all)", len(cfg.whitelist))
		}
	})

	t.Run("mixed valid and invalid", func(t *testing.T) {
		t.Setenv("WHITELISTED_SUBNET", "10.0.0.0/8,not-a-cidr,172.16.0.0/12")
		cfg := parseConfig()
		if len(cfg.whitelist) != 2 {
			t.Errorf("mixed: got %d CIDRs, want 2 (invalid skipped)", len(cfg.whitelist))
		}
	})

	t.Run("whitespace around entries", func(t *testing.T) {
		t.Setenv("WHITELISTED_SUBNET", "  10.0.0.0/8  ,  172.16.0.0/12  ")
		cfg := parseConfig()
		if len(cfg.whitelist) != 2 {
			t.Errorf("whitespace: got %d CIDRs, want 2", len(cfg.whitelist))
		}
	})
}

// ---------------------------------------------------------------------------
// Edge case: upstream unreachable
// ---------------------------------------------------------------------------

func TestProxyUpstreamUnreachable(t *testing.T) {
	// Point at a port nothing is listening on.
	cfg := proxyTestConfig("127.0.0.1:19999", "127.0.0.0/8")
	cfg.dialTimeout = 100 * time.Millisecond

	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	// Connection should be accepted by the proxy, but the upstream dial
	// will fail. The proxy should close the client connection cleanly.
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	n, _ := conn.Read(make([]byte, 64))
	conn.Close()

	if n != 0 {
		t.Errorf("read %d bytes from failed-upstream connection, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// Edge case: zero-length payload
// ---------------------------------------------------------------------------

func TestProxyZeroLengthPayload(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	cfg := proxyTestConfig(upstreamAddr, "127.0.0.0/8")
	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Close immediately without sending anything.
	if tcp, ok := conn.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}

	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(reply) != 0 {
		t.Errorf("expected empty reply, got %d bytes", len(reply))
	}
}

// ---------------------------------------------------------------------------
// Edge case: single-byte payload
// ---------------------------------------------------------------------------

func TestProxySingleBytePayload(t *testing.T) {
	upstreamAddr, upstreamStop := echoServer(t)
	defer upstreamStop()

	cfg := proxyTestConfig(upstreamAddr, "127.0.0.0/8")
	proxyAddr, proxyStop := startProxy(t, cfg)
	defer proxyStop()

	reply := sendRecv(t, proxyAddr, "X")
	if reply != "X" {
		t.Errorf("single byte: got %q, want %q", reply, "X")
	}
}

// ---------------------------------------------------------------------------
// Edge case: concurrent buffer pool access
// ---------------------------------------------------------------------------

func TestBufPoolConcurrent(t *testing.T) {
	const goroutines = 100
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				buf := bufPool.Get().([]byte)
				if len(buf) != defaultCopyBufSize {
					t.Errorf("buffer size %d != %d", len(buf), defaultCopyBufSize)
				}
				// Write something to simulate real use.
				buf[0] = byte(i)
				bufPool.Put(buf)
			}
		}()
	}
	wg.Wait()
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

	cfg := proxyTestConfig(upstreamAddr, "127.0.0.0/8")
	cfg.idleTimeout = 5 * time.Minute
	proxyAddr, proxyStop := startProxy(b, cfg)
	defer proxyStop()

	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	b.ResetTimer()
	b.SetBytes(int64(len(payload) * 2)) // sent + echoed

	for i := 0; i < b.N; i++ {
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
}
