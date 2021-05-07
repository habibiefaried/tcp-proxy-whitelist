package main

import (
	"fmt"
	"io"
	"net"
	"os"
)

func main() {
	if (os.Getenv("BIND_PORT") == "") || (os.Getenv("REMOTE_ADDR_PAIR") == "") {
		panic("env BIND_PORT and REMOTE_ADDR_PAIR must be set. REMOTE_ADDR_PAIR have this format `<ip:port>`")
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
			fmt.Printf("error accepting connection: %v", err)
			continue
		}
		go func() {
			conn2, err := net.Dial("tcp", os.Getenv("REMOTE_ADDR_PAIR"))
			if err != nil {
				fmt.Printf("error dialing remote addr %v", err)
				return
			}
			go io.Copy(conn2, conn)
			io.Copy(conn, conn2)
			conn2.Close()
			conn.Close()
		}()
	}
}
