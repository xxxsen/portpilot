package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {
	os.Exit(mainCode())
}

func mainCode() int {
	tcpAddress := flag.String("tcp", "127.0.0.1:18080", "TCP echo address")
	udpAddress := flag.String("udp", "127.0.0.1:18081", "UDP echo address")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *tcpAddress, *udpAddress); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, tcpAddress, udpAddress string) error {
	tcpListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", tcpAddress)
	if err != nil {
		return err
	}
	defer func() { _ = tcpListener.Close() }()
	resolved, err := net.ResolveUDPAddr("udp", udpAddress)
	if err != nil {
		return err
	}
	udpConn, err := net.ListenUDP("udp", resolved)
	if err != nil {
		return err
	}
	defer func() { _ = udpConn.Close() }()
	fmt.Printf("echo fixture ready tcp=%s udp=%s\n", tcpAddress, udpAddress)
	errCh := make(chan error, 2)
	go serveTCP(tcpListener, errCh)
	go serveUDP(udpConn, errCh)
	go func() {
		<-ctx.Done()
		_ = tcpListener.Close()
		_ = udpConn.Close()
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

func serveTCP(listener net.Listener, errCh chan<- error) {
	var clients sync.WaitGroup
	defer clients.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		clients.Add(1)
		go func() {
			defer clients.Done()
			defer func() { _ = conn.Close() }()
			_, _ = io.Copy(conn, conn)
		}()
	}
}

func serveUDP(conn *net.UDPConn, errCh chan<- error) {
	buffer := make([]byte, 65507)
	for {
		n, address, err := conn.ReadFromUDP(buffer)
		if err != nil {
			errCh <- err
			return
		}
		if _, err := conn.WriteToUDP(buffer[:n], address); err != nil {
			errCh <- err
			return
		}
	}
}
