package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

func main() {
	os.Exit(mainCode())
}

func mainCode() int {
	tcpAddress := flag.String("tcp", "127.0.0.1:18080", "TCP echo address")
	proxyTCPAddress := flag.String("proxy-tcp", "127.0.0.1:18082", "PROXY protocol v1 TCP echo address")
	udpAddress := flag.String("udp", "127.0.0.1:18081", "UDP echo address")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *tcpAddress, *proxyTCPAddress, *udpAddress); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, tcpAddress, proxyTCPAddress, udpAddress string) error {
	tcpListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", tcpAddress)
	if err != nil {
		return err
	}
	defer func() { _ = tcpListener.Close() }()
	proxyTCPListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", proxyTCPAddress)
	if err != nil {
		return err
	}
	defer func() { _ = proxyTCPListener.Close() }()
	resolved, err := net.ResolveUDPAddr("udp", udpAddress)
	if err != nil {
		return err
	}
	udpConn, err := net.ListenUDP("udp", resolved)
	if err != nil {
		return err
	}
	defer func() { _ = udpConn.Close() }()
	fmt.Printf("echo fixture ready tcp=%s proxy_tcp=%s udp=%s\n", tcpAddress, proxyTCPAddress, udpAddress)
	errCh := make(chan error, 3)
	go serveTCP(tcpListener, false, errCh)
	go serveTCP(proxyTCPListener, true, errCh)
	go serveUDP(udpConn, errCh)
	go func() {
		<-ctx.Done()
		_ = tcpListener.Close()
		_ = proxyTCPListener.Close()
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

func serveTCP(listener net.Listener, expectProxyProtocol bool, errCh chan<- error) {
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
			if !expectProxyProtocol {
				_, _ = io.Copy(conn, conn)
				return
			}
			reader := bufio.NewReaderSize(conn, 108)
			header, err := reader.ReadString('\n')
			if err != nil || len(header) > 107 ||
				!strings.HasPrefix(header, "PROXY TCP") ||
				!strings.HasSuffix(header, "\r\n") {
				return
			}
			_, _ = io.Copy(conn, reader)
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
