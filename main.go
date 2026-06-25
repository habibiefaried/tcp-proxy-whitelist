// tcp-proxy-whitelist is a minimal, zero-dependency TCP proxy that only forwards
// connections from IPs matching a whitelist of CIDR subnets. It binds to a
// configurable port and proxies accepted connections to an upstream server.
//
// All configuration is read from environment variables:
//
//	BIND_PORT          (required)  port to listen on, bound to 0.0.0.0
//	REMOTE_ADDR_PAIR   (required)  upstream target in ip:port format
//	WHITELISTED_SUBNET (optional)  comma-separated CIDRs (empty = block all)
//
// Example:
//
//	BIND_PORT=9090 REMOTE_ADDR_PAIR=10.0.0.5:8080 \
//	  WHITELISTED_SUBNET=10.0.0.0/8,172.16.0.0/12 ./wlistproxy
package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	// Prepend a consistent tag to all log lines so the proxy is identifiable
	// when multiple services log to the same stream (e.g. journald).
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[wlistproxy] ")

	// -----------------------------------------------------------------------
	// Configuration — all via environment variables (12-factor style).
	// No config files, no CLI flags.  Keeps deployment dead simple.
	// -----------------------------------------------------------------------
	cfg := parseConfig()

	// -----------------------------------------------------------------------
	// Listener — on Linux this enables SO_REUSEPORT so you can run N
	// instances (one per CPU core) and the kernel distributes connections.
	// On other platforms it falls back to a plain net.Listen.
	// -----------------------------------------------------------------------
	listener, err := tuneListener("tcp", net.JoinHostPort("0.0.0.0", cfg.bindPort))
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Printf("listening on %s, proxying to %s", listener.Addr(), cfg.remoteAddr)

	// -----------------------------------------------------------------------
	// Graceful shutdown machinery.
	//
	// We need two things:
	//   1. A context that cancels when a shutdown signal arrives.
	//   2. A WaitGroup that tracks every in-flight connection goroutine.
	//
	// When SIGINT or SIGTERM arrives:
	//   - The signal goroutine cancels the context.
	//   - It ALSO closes the listener (this makes Accept return an error,
	//     which is how the main loop breaks out).
	//   - The main loop sees ctx.Done(), then waits for all connection
	//     goroutines to finish before exiting.
	//
	// This guarantees no connection is dropped mid-transfer during shutdown.
	// -----------------------------------------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// Register for OS signals.  We use a buffered channel (size 1) so the
	// signal send is non-blocking — the runtime drops signals if the buffer
	// is full, so 1 is enough since we only care about the first signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received %v, shutting down...", sig)
		cancel()         // unblock ctx.Done() in the accept loop
		listener.Close() // unblock listener.Accept()
	}()

	// -----------------------------------------------------------------------
	// Accept loop — runs until the listener is closed by the signal handler.
	// Each accepted connection gets its own goroutine so blocking I/O on one
	// client never stalls another.
	// -----------------------------------------------------------------------
	for {
		conn, err := listener.Accept()
		if err != nil {
			// If the context is done, the error came from our own
			// listener.Close() call — that's a clean shutdown.
			// Otherwise it's a transient accept error (e.g. EMFILE
			// under load) and we should keep trying.
			select {
			case <-ctx.Done():
				log.Println("waiting for active connections to drain...")
				wg.Wait()
				log.Println("shutdown complete")
				return
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}

		// Track this goroutine so shutdown waits for it.
		wg.Add(1)
		go func() {
			defer wg.Done()
			handleConnection(conn, cfg.remoteAddr, cfg.whitelist, cfg.dialTimeout, cfg.idleTimeout)
		}()
	}
}

// config holds the parsed runtime configuration.
// Fields are populated from environment variables by parseConfig.
type config struct {
	// bindPort is the TCP port the proxy listens on (e.g. "9090").
	bindPort string

	// remoteAddr is the upstream server address in ip:port form.
	remoteAddr string

	// whitelist is the set of allowed CIDR subnets.
	// An empty/nil slice means "block everything".
	whitelist []*net.IPNet

	// dialTimeout caps how long we wait for the upstream TCP handshake.
	dialTimeout time.Duration

	// idleTimeout is the maximum lifetime of a proxied connection.
	// When the deadline is reached the relay is torn down, preventing
	// goroutine leaks from protocols that use indefinite keep-alive
	// (e.g. HTTP/1.1 without Connection: close).
	// Default: 30s. Set via IDLE_TIMEOUT (e.g. "60s", "5m").
	idleTimeout time.Duration
}

// parseConfig reads configuration from environment variables.
// Missing required values (BIND_PORT, REMOTE_ADDR_PAIR) cause a fatal exit.
// Invalid CIDRs in WHITELISTED_SUBNET are skipped with a warning.
func parseConfig() *config {
	bindPort := os.Getenv("BIND_PORT")
	remoteAddr := os.Getenv("REMOTE_ADDR_PAIR")

	if bindPort == "" || remoteAddr == "" {
		log.Fatal(
			"BIND_PORT and REMOTE_ADDR_PAIR must be set. " +
				"REMOTE_ADDR_PAIR format: <ip:port>",
		)
	}

	cfg := &config{
		bindPort:    bindPort,
		remoteAddr:  remoteAddr,
		dialTimeout: 10 * time.Second,
		idleTimeout: 30 * time.Second,
	}

	// Optional: IDLE_TIMEOUT caps the lifetime of each proxied connection
	// to prevent goroutine leaks from keep-alive protocols.
	if s := os.Getenv("IDLE_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			log.Printf("invalid IDLE_TIMEOUT %q, using default 30s: %v", s, err)
		} else {
			cfg.idleTimeout = d
		}
	}

	// Parse the optional whitelist.
	//   - Empty string → block all connections (whitelist stays nil).
	//   - "0.0.0.0/0" → allow all IPv4 (still blocks IPv6 unless ::/0 is added).
	//   - "10.0.0.0/8,172.16.0.0/12" → allow RFC 1918 private ranges.
	whitelistEnv := os.Getenv("WHITELISTED_SUBNET")
	if whitelistEnv == "" {
		log.Println("[WARN] WHITELISTED_SUBNET is empty — blocking all connections.")
	} else {
		// Split on commas, trim whitespace, parse each CIDR.
		// Invalid entries are logged and skipped rather than aborting,
		// so a single typo doesn't take down the proxy.
		for _, s := range strings.Split(whitelistEnv, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			_, cidr, err := net.ParseCIDR(s)
			if err != nil {
				log.Printf("skipping invalid CIDR %q: %v", s, err)
				continue
			}
			cfg.whitelist = append(cfg.whitelist, cidr)
		}
		if len(cfg.whitelist) == 0 {
			log.Println("[WARN] no valid CIDRs in WHITELISTED_SUBNET — blocking all connections.")
		}
	}

	return cfg
}
