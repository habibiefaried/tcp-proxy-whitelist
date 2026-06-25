package main

import (
	"context"
	"io"
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

	bindPort := os.Getenv("BIND_PORT")
	remoteAddr := os.Getenv("REMOTE_ADDR_PAIR")
	whitelistEnv := os.Getenv("WHITELISTED_SUBNET")

	if bindPort == "" || remoteAddr == "" {
		log.Fatal("BIND_PORT and REMOTE_ADDR_PAIR must be set. REMOTE_ADDR_PAIR format: <ip:port>")
	}

	// Parse whitelisted CIDR subnets.
	var whitelist []*net.IPNet
	if whitelistEnv == "" {
		log.Println("[WARN] WHITELISTED_SUBNET is empty, blocking all connections.")
	} else {
		for _, s := range strings.Split(whitelistEnv, ",") {
			s = strings.TrimSpace(s)
			_, cidr, err := net.ParseCIDR(s)
			if err != nil {
				log.Printf("error parsing CIDR %q: %v", s, err)
				continue
			}
			whitelist = append(whitelist, cidr)
		}
		if len(whitelist) == 0 {
			log.Println("[WARN] no valid CIDRs in WHITELISTED_SUBNET, blocking all connections.")
		}
	}

	localAddr := net.JoinHostPort("0.0.0.0", bindPort)
	log.Printf("listening on %s, proxying to %s", localAddr, remoteAddr)

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		log.Fatal(err)
	}

	// Graceful shutdown via context cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received signal %v, shutting down...", sig)
		cancel()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				log.Println("waiting for active connections to finish...")
				wg.Wait()
				log.Println("shutdown complete")
				return
			default:
				log.Printf("error accepting connection: %v", err)
				continue
			}
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			handleConnection(conn, remoteAddr, whitelist)
		}()
	}
}

// handleConnection checks the whitelist and proxies the connection upstream.
func handleConnection(client net.Conn, remoteAddr string, whitelist []*net.IPNet) {
	defer client.Close()

	tcpAddr, ok := client.RemoteAddr().(*net.TCPAddr)
	if !ok {
		log.Println("cannot cast remote addr to TCP, skipping")
		return
	}
	remoteIP := tcpAddr.IP

	log.Printf("connection from %s", remoteIP)

	if !isWhitelisted(remoteIP, whitelist) {
		log.Printf("rejected: %s not in whitelist", remoteIP)
		return
	}

	upstream, err := net.DialTimeout("tcp", remoteAddr, 10*time.Second)
	if err != nil {
		log.Printf("error dialing upstream %s: %v", remoteAddr, err)
		return
	}
	defer upstream.Close()

	log.Printf("proxying %s -> %s", remoteIP, remoteAddr)

	// Bidirectional copy. When one direction finishes, close the other to
	// unblock and avoid leaking goroutines.
	var pipe sync.WaitGroup
	pipe.Add(2)
	go func() {
		defer pipe.Done()
		io.Copy(upstream, client)
		// Signal the other direction that we're done reading from the client.
		if tcp, ok := upstream.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}()
	go func() {
		defer pipe.Done()
		io.Copy(client, upstream)
		// Signal the other direction that we're done reading from upstream.
		if tcp, ok := client.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}()
	pipe.Wait()

	log.Printf("connection closed: %s", remoteIP)
}

// isWhitelisted returns true if ip is contained within any of the CIDR subnets.
// An empty whitelist always returns false (blocks everything).
func isWhitelisted(ip net.IP, whitelist []*net.IPNet) bool {
	for _, cidr := range whitelist {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
