package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"time"
)

func echoUDP(
	ctx context.Context,
	conn *net.UDPConn,
	target *net.UDPAddr,
	payload []byte,
	buffer []byte,
) (int, bool) {
	deadline := time.Now().Add(5 * time.Second)
	retries := 0
	for {
		if ctx.Err() != nil {
			return retries, false
		}
		attemptDeadline := time.Now().Add(250 * time.Millisecond)
		if attemptDeadline.After(deadline) {
			attemptDeadline = deadline
		}
		_ = conn.SetDeadline(attemptDeadline)
		if _, err := conn.WriteToUDP(payload, target); err == nil {
			for {
				n, _, err := conn.ReadFromUDP(buffer)
				if err != nil {
					if !errors.Is(err, net.ErrClosed) {
						break
					}
					return retries, false
				}
				if bytes.Equal(payload, buffer[:n]) {
					return retries, true
				}
			}
		}
		if !time.Now().Before(deadline) {
			return retries, false
		}
		retries++
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return retries, false
		case <-timer.C:
		}
	}
}
