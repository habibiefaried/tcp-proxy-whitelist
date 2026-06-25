//go:build linux

package main

import (
	"context"
	"net"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// setQuickAck enables TCP_QUICKACK so the kernel ACKs data immediately instead of
// waiting for a delayed-ACK timer (which can add up to 40ms per direction).
func setQuickAck(conn *net.TCPConn) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	raw.Control(func(fd uintptr) {
		unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_QUICKACK, 1)
	})
}

// relay uses splice(2) for zero-copy socket-to-socket data transfer in the kernel.
// Falls back to bufferRelay if splice setup fails (pipe exhaustion, non-socket FDs).
func relay(a, b *net.TCPConn) {
	if err := spliceRelay(a, b); err != nil {
		bufferRelay(a, b)
	}
}

// spliceRelay performs bidirectional zero-copy relay via splice(2) and kernel pipes.
//
// Two pipes are used — one per direction. For each direction:
//  1. splice(socket → pipe_write)  — move data into pipe (kernel memory)
//  2. splice(pipe_read → socket)   — move data into destination socket
//
// Data never crosses the userspace boundary. This is the same primitive HAProxy
// and nginx ride on for L4 TCP proxying.
func spliceRelay(client, upstream *net.TCPConn) error {
	var p1, p2 [2]int
	if err := unix.Pipe2(p1[:], unix.O_CLOEXEC); err != nil {
		return err
	}
	if err := unix.Pipe2(p2[:], unix.O_CLOEXEC); err != nil {
		unix.Close(p1[0])
		unix.Close(p1[1])
		return err
	}
	defer func() {
		unix.Close(p1[0]); unix.Close(p1[1])
		unix.Close(p2[0]); unix.Close(p2[1])
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// client → upstream (pipe p1)
	go func() {
		defer wg.Done()
		spliceDirection(upstream, client, p1[1], p1[0])
		upstream.CloseWrite()
	}()

	// upstream → client (pipe p2)
	go func() {
		defer wg.Done()
		spliceDirection(client, upstream, p2[1], p2[0])
		client.CloseWrite()
	}()

	// Wait for both directions to finish (peer closes → CloseWrite signals
	// the other direction → splice returns on error → goroutine exits).
	wg.Wait()
	return nil
}

const maxSpliceSize = 1 << 20 // 1 MiB per splice call

// spliceDirection copies data from src to dst through an in-kernel pipe.
//
//	pipeWrite — write end (src → pipe)
//	pipeRead  — read end  (pipe → dst)
func spliceDirection(dst, src *net.TCPConn, pipeWrite, pipeRead int) {
	var srcFD, dstFD int

	srcRaw, err := src.SyscallConn()
	if err != nil {
		return
	}
	srcRaw.Control(func(fd uintptr) { srcFD = int(fd) })

	dstRaw, err := dst.SyscallConn()
	if err != nil {
		return
	}
	dstRaw.Control(func(fd uintptr) { dstFD = int(fd) })

	for {
		// Step 1: splice from source socket into the write end of the pipe.
		n, err := unix.Splice(srcFD, nil, pipeWrite, nil, maxSpliceSize,
			unix.SPLICE_F_MOVE|unix.SPLICE_F_MORE)
		if n < 0 {
			if err == unix.EAGAIN {
				// Socket not ready — wait with poll, then retry.
				if pollReadable(srcFD) != nil {
					return
				}
				continue
			}
			return // unrecoverable error
		}
		if n == 0 {
			return // EOF — peer closed write side
		}

		// Step 2: splice from pipe into destination socket.
		// Loop to handle short writes (should be rare with SPLICE_F_MOVE).
		remaining := int(n)
		for remaining > 0 {
			wn, werr := unix.Splice(pipeRead, nil, dstFD, nil, remaining,
				unix.SPLICE_F_MOVE|unix.SPLICE_F_MORE)
			if wn <= 0 {
				if werr == unix.EAGAIN {
					if pollWritable(dstFD) != nil {
						return
					}
					continue
				}
				return
			}
			remaining -= int(wn)
		}
	}
}

// pollReadable blocks until fd is readable (POLLIN) or an error occurs.
func pollReadable(fd int) error {
	var fds [1]unix.PollFd
	fds[0] = unix.PollFd{Fd: int32(fd), Events: unix.POLLIN}
	_, err := unix.Poll(fds[:], 100) // 100ms timeout
	return err
}

// pollWritable blocks until fd is writable (POLLOUT) or an error occurs.
func pollWritable(fd int) error {
	var fds [1]unix.PollFd
	fds[0] = unix.PollFd{Fd: int32(fd), Events: unix.POLLOUT}
	_, err := unix.Poll(fds[:], 100) // 100ms timeout
	return err
}

// tuneListener creates a TCP listener with SO_REUSEPORT enabled so multiple
// processes can bind the same port for linear multi-core scaling.
func tuneListener(network, address string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			c.Control(func(fd uintptr) {
				opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			})
			return opErr
		},
	}
	return lc.Listen(context.Background(), network, address)
}
