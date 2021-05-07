package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

func main() {
	var whitelisted_subarray []*net.IPNet

	if (os.Getenv("BIND_PORT") == "") || (os.Getenv("REMOTE_ADDR_PAIR") == "") {
		panic("env BIND_PORT and REMOTE_ADDR_PAIR must be set. REMOTE_ADDR_PAIR have this format `<ip:port>`")
	}

	if os.Getenv("WHITELISTED_SUBNET") == "" {
		fmt.Println("[WARN] env WHITELISTED_SUBNET is empty, blocking all connections..")
	} else {
		for _, v := range strings.Split(os.Getenv("WHITELISTED_SUBNET"), ",") {
			_, ipnetB, err := net.ParseCIDR(v)
			if err != nil {
				fmt.Printf("error parse %v\n", err)
			} else {
				whitelisted_subarray = append(whitelisted_subarray, ipnetB)
			}
		}
	}

	localAddr := "0.0.0.0:" + os.Getenv("BIND_PORT")
	fmt.Printf("Listening: %v\nProxying: %v\n\n", localAddr, os.Getenv("REMOTE_ADDR_PAIR"))

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		panic(err)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("error accepting connection: %v\n", err)
			conn.Close()
			continue
		}

		if addr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
			IsWhitelisted := false

			fmt.Println("Connected from " + addr.IP.String())
			for _, v := range whitelisted_subarray {
				if v.Contains(addr.IP) {
					IsWhitelisted = true
					break
				}
			}

			if !IsWhitelisted {
				fmt.Println("Not eligible")
				conn.Close()
				continue
			}

		} else {
			fmt.Println("cannot cast the remote addr to TCP. Skipping..")
			conn.Close()
			continue
		}

		go func() {
			conn2, err := net.Dial("tcp", os.Getenv("REMOTE_ADDR_PAIR"))
			if err != nil {
				fmt.Printf("error dialing remote addr %v\n", err)
				return
			}
			go io.Copy(conn2, conn)
			io.Copy(conn, conn2)
			conn2.Close()
			conn.Close()
		}()
	}
}
