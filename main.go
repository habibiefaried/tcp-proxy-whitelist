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
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[wlistproxy] ")

	cfg := parseConfig()

	listener, err := tuneListener("tcp", net.JoinHostPort("0.0.0.0", cfg.bindPort))
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Printf("listening on %s, proxying to %s", listener.Addr(), cfg.remoteAddr)

	// Graceful shutdown: cancel context on SIGINT/SIGTERM, wait for active connections.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received %v, shutting down...", sig)
		cancel()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
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

		wg.Add(1)
		go func() {
			defer wg.Done()
			handleConnection(conn, cfg.remoteAddr, cfg.whitelist, cfg.dialTimeout)
		}()
	}
}

// config holds the parsed runtime configuration.
type config struct {
	bindPort    string
	remoteAddr  string
	whitelist   []*net.IPNet
	dialTimeout time.Duration
}

// parseConfig reads configuration from environment variables.
// It calls log.Fatal for missing required values.
func parseConfig() *config {
	bindPort := os.Getenv("BIND_PORT")
	remoteAddr := os.Getenv("REMOTE_ADDR_PAIR")

	if bindPort == "" || remoteAddr == "" {
		log.Fatal("BIND_PORT and REMOTE_ADDR_PAIR must be set. REMOTE_ADDR_PAIR format: <ip:port>")
	}

	cfg := &config{
		bindPort:    bindPort,
		remoteAddr:  remoteAddr,
		dialTimeout: 10 * time.Second,
	}

	whitelistEnv := os.Getenv("WHITELISTED_SUBNET")
	if whitelistEnv == "" {
		log.Println("[WARN] WHITELISTED_SUBNET is empty — blocking all connections.")
	} else {
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
