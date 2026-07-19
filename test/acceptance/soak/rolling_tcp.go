package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

func runPersistentTCP(
	ctx context.Context,
	concurrency int,
	deadline time.Time,
	holdDuration time.Duration,
	result *counters,
) {
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func(workerID int) {
			defer workers.Done()
			runPersistentTCPWorker(ctx, workerID, deadline, holdDuration, result)
		}(worker)
	}
	workers.Wait()
}

func runPersistentTCPWorker(
	ctx context.Context,
	workerID int,
	deadline time.Time,
	holdDuration time.Duration,
	result *counters,
) {
	generation := 0
	sequence := 0
	for time.Now().Before(deadline) {
		generation++
		windowEnd := rollingWindowEnd(deadline, holdDuration, workerID, generation)
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", tcpProxy)
		if err != nil {
			result.tcpConnectRetries.Add(1)
			if !waitTCPRetry(ctx, deadline) {
				return
			}
			continue
		}

		established, unexpectedClose, canceled := echoPersistentTCP(
			ctx,
			conn,
			windowEnd,
			workerID,
			&sequence,
			result,
		)
		_ = conn.Close()
		if canceled {
			return
		}
		if unexpectedClose {
			result.failed.Add(1)
			return
		}
		if !established {
			result.tcpConnectRetries.Add(1)
			if !waitTCPRetry(ctx, deadline) {
				return
			}
		}
	}
}

func rollingWindowEnd(
	deadline time.Time,
	holdDuration time.Duration,
	workerID int,
	generation int,
) time.Time {
	windowEnd := time.Now().Add(persistentWindow(holdDuration, workerID, generation))
	if windowEnd.After(deadline) {
		return deadline
	}
	return windowEnd
}

func echoPersistentTCP(
	ctx context.Context,
	conn net.Conn,
	windowEnd time.Time,
	workerID int,
	sequence *int,
	result *counters,
) (established bool, unexpectedClose bool, canceled bool) {
	for time.Now().Before(windowEnd) {
		*sequence++
		payload := []byte(fmt.Sprintf("portpilot-long-%d-%d", workerID, *sequence))
		if !echoTCP(conn, payload) {
			return established, established, false
		}
		if !established {
			established = true
			result.tcpLongConnections.Add(1)
		}
		result.tcpLong.Add(1)
		if !waitPersistentTick(ctx) {
			return established, false, true
		}
	}
	return established, false, false
}

func waitPersistentTick(ctx context.Context) bool {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
