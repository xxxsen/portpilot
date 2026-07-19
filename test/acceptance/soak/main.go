package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	tcpProxy = "127.0.0.1:7000"
	udpProxy = "127.0.0.1:7001"
)

type counters struct {
	tcpShort           atomic.Int64
	tcpLong            atomic.Int64
	tcpLongConnections atomic.Int64
	tcpConnectRetries  atomic.Int64
	udp                atomic.Int64
	udpRetries         atomic.Int64
	chaos              atomic.Int64
	failed             atomic.Int64
}

func main() {
	duration := envDuration("ACCEPT_SOAK_DURATION", 30*time.Minute)
	concurrency := envInt("ACCEPT_TCP_CONCURRENCY", 128)
	tcpCycles := envInt("ACCEPT_TCP_CYCLES", 1000)
	udpFlows := envInt("ACCEPT_UDP_FLOWS", 128)
	tcpHoldDuration := envDuration("ACCEPT_TCP_HOLD_DURATION", 20*time.Second)
	udpDatagrams := envInt("ACCEPT_UDP_DATAGRAMS", 10000)
	chaosInterval := envDuration("ACCEPT_CHAOS_INTERVAL", 2*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var result counters

	runShortTCP(ctx, concurrency, tcpCycles, &result)
	if result.failed.Load() != 0 {
		fail(&result, "short TCP phase")
	}

	admin, err := newChaosAdmin(ctx)
	if err != nil {
		result.failed.Add(1)
		fmt.Fprintf(os.Stderr, "prepare soak control chaos: %v\n", err)
		fail(&result, "control preparation")
	}
	if err := admin.waitForRecovery(ctx); err != nil {
		result.failed.Add(1)
		fmt.Fprintf(os.Stderr, "prepare soak resource baseline: %v\n", err)
		fail(&result, "resource baseline preparation")
	}
	initialResources, err := admin.summary(ctx)
	if err != nil {
		result.failed.Add(1)
		fmt.Fprintf(os.Stderr, "read soak resource baseline: %v\n", err)
		fail(&result, "resource baseline")
	}
	deadline := time.Now().Add(duration)
	loadCtx, stopLoad := context.WithCancel(ctx)
	defer stopLoad()
	monitorDone := make(chan struct{})
	go stopOnFailure(ctx, stopLoad, monitorDone, &result)
	var phases sync.WaitGroup
	phases.Add(3)
	go func() {
		defer phases.Done()
		runPersistentTCP(loadCtx, concurrency, deadline, tcpHoldDuration, &result)
	}()
	go func() {
		defer phases.Done()
		runUDP(loadCtx, udpFlows, udpDatagrams, deadline, &result)
	}()
	go func() {
		defer phases.Done()
		runControlChaos(loadCtx, admin, deadline, chaosInterval, &result)
	}()
	phases.Wait()
	close(monitorDone)
	stopLoad()
	if result.failed.Load() != 0 {
		fail(&result, "stability phase")
	}
	if result.udp.Load() != int64(udpDatagrams) {
		result.failed.Add(1)
		fail(&result, "UDP completion")
	}
	if err := admin.waitForRecovery(ctx); err != nil {
		result.failed.Add(1)
		fmt.Fprintf(os.Stderr, "wait for soak recovery: %v\n", err)
		fail(&result, "runtime recovery")
	}
	finalResources, err := admin.summary(ctx)
	if err != nil {
		result.failed.Add(1)
		fmt.Fprintf(os.Stderr, "read final soak resources: %v\n", err)
		fail(&result, "resource recovery")
	}
	if err := verifyResourceRecovery(initialResources, finalResources); err != nil {
		result.failed.Add(1)
		fmt.Fprintf(os.Stderr, "verify soak resources: %v\n", err)
		fail(&result, "resource recovery")
	}
	fmt.Printf(
		"soak passed: duration=%s tcp_short=%d tcp_long=%d tcp_long_connections=%d tcp_connect_retries=%d udp_datagrams=%d udp_retries=%d chaos_rotations=%d concurrency=%d udp_flows=%d\n",
		duration, result.tcpShort.Load(), result.tcpLong.Load(), result.tcpLongConnections.Load(),
		result.tcpConnectRetries.Load(), result.udp.Load(), result.udpRetries.Load(),
		result.chaos.Load(), concurrency, udpFlows,
	)
	fmt.Printf(
		"soak resources recovered: goroutines=%d->%d open_fds=%d->%d heap_inuse_bytes=%d->%d\n",
		initialResources.Goroutines, finalResources.Goroutines,
		initialResources.OpenFDs, finalResources.OpenFDs,
		initialResources.HeapInuseBytes, finalResources.HeapInuseBytes,
	)
}

func stopOnFailure(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, result *counters) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if result.failed.Load() != 0 {
				cancel()
				return
			}
		}
	}
}

func runShortTCP(ctx context.Context, concurrency, cycles int, result *counters) {
	var next atomic.Int64
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func(workerID int) {
			defer workers.Done()
			for {
				cycle := int(next.Add(1))
				if cycle > cycles {
					return
				}
				payload := []byte(fmt.Sprintf("portpilot-short-%d-%d", workerID, cycle))
				conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", tcpProxy)
				if err != nil {
					result.failed.Add(1)
					continue
				}
				if echoTCP(conn, payload) {
					result.tcpShort.Add(1)
				} else {
					result.failed.Add(1)
				}
				_ = conn.Close()
			}
		}(worker)
	}
	workers.Wait()
}

func persistentWindow(base time.Duration, workerID, generation int) time.Duration {
	spread := base / 2
	slot := time.Duration((workerID*7 + generation*3) % 11)
	return base*3/4 + spread*slot/10
}

func waitTCPRetry(ctx context.Context, deadline time.Time) bool {
	if !time.Now().Before(deadline) {
		return false
	}
	timer := time.NewTimer(100 * time.Millisecond)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false
	case <-timer.C:
		return true
	}
}

func echoTCP(conn net.Conn, payload []byte) bool {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		return false
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		return false
	}
	return bytes.Equal(payload, response) && sha256.Sum256(payload) == sha256.Sum256(response)
}

func runUDP(
	ctx context.Context,
	flows int,
	datagrams int,
	deadline time.Time,
	result *counters,
) {
	target, err := net.ResolveUDPAddr("udp", udpProxy)
	if err != nil {
		result.failed.Add(1)
		return
	}
	perFlow := (datagrams + flows - 1) / flows
	pace := time.Until(deadline) * 4 / 5 / time.Duration(perFlow)
	var next atomic.Int64
	var workers sync.WaitGroup
	for flow := 0; flow < flows; flow++ {
		workers.Add(1)
		go func(flowID int) {
			defer workers.Done()
			conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				result.failed.Add(1)
				return
			}
			defer func() { _ = conn.Close() }()
			buffer := make([]byte, 65507)
			for {
				sequence := int(next.Add(1))
				if sequence > datagrams {
					return
				}
				payload := []byte(fmt.Sprintf("portpilot-udp-%d-%d", flowID, sequence))
				retries, ok := echoUDP(ctx, conn, target, payload, buffer)
				result.udpRetries.Add(int64(retries))
				if !ok {
					result.failed.Add(1)
					continue
				}
				result.udp.Add(1)
				if pace > 0 {
					timer := time.NewTimer(pace)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
		}(flow)
	}
	workers.Wait()
}

func fail(result *counters, phase string) {
	fmt.Fprintf(
		os.Stderr,
		"soak failed in %s: tcp_short=%d tcp_long=%d tcp_long_connections=%d tcp_connect_retries=%d udp_datagrams=%d udp_retries=%d chaos_rotations=%d failures=%d\n",
		phase, result.tcpShort.Load(), result.tcpLong.Load(), result.tcpLongConnections.Load(),
		result.tcpConnectRetries.Load(), result.udp.Load(), result.udpRetries.Load(),
		result.chaos.Load(), result.failed.Load(),
	)
	os.Exit(1)
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if value := os.Getenv(name); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value > 0 {
		return value
	}
	return fallback
}
