package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

type smokeSummary struct {
	PendingTunnels int   `json:"pending_tunnels"`
	ActiveTunnels  int64 `json:"active_tunnels"`
	UDPChannels    int   `json:"udp_channels"`
	UDPFlows       int   `json:"udp_flows"`
}

type smokeConnection struct {
	AcceptedAt  int64  `json:"accepted_at"`
	Protocol    string `json:"protocol"`
	ProxyID     string `json:"proxy_id"`
	UpBytes     int64  `json:"up_bytes"`
	DownBytes   int64  `json:"down_bytes"`
	UpPackets   int64  `json:"up_packets"`
	DownPackets int64  `json:"down_packets"`
}

func smokeObservability(ctx context.Context, adminURL string, since time.Time) error {
	client, err := smokeAdminClient(ctx, adminURL)
	if err != nil {
		return err
	}
	for {
		var summary smokeSummary
		if err := smokeGetJSON(ctx, client, adminURL+"/api/metrics/summary", &summary); err != nil {
			return err
		}
		var payload struct {
			Items []smokeConnection `json:"items"`
		}
		if err := smokeGetJSON(ctx, client, adminURL+"/api/network/connections?page=1&page_size=100", &payload); err != nil {
			return err
		}
		tcpConnections := map[string]bool{}
		udpFlows := 0
		for _, connection := range payload.Items {
			if connection.AcceptedAt < since.UnixMilli() {
				continue
			}
			if connection.Protocol == "tcp" && connection.UpBytes > 0 && connection.DownBytes > 0 {
				tcpConnections[connection.ProxyID] = true
			}
			if connection.Protocol == "udp" &&
				connection.UpBytes > 0 && connection.DownBytes > 0 &&
				connection.UpPackets > 0 && connection.DownPackets > 0 {
				udpFlows++
			}
		}
		if summary.PendingTunnels == 0 && summary.ActiveTunnels == 0 &&
			summary.UDPChannels >= 2 && summary.UDPFlows >= 2 &&
			tcpConnections["dev-tcp-echo"] && tcpConnections["dev-tcp-direct"] &&
			udpFlows >= 2 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"observability did not converge: pending=%d active=%d udp_channels=%d udp_flows=%d tcp_connections=%v observed_udp_flows=%d",
				summary.PendingTunnels, summary.ActiveTunnels,
				summary.UDPChannels, summary.UDPFlows, tcpConnections, udpFlows,
			)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func smokeAdminClient(ctx context.Context, adminURL string) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		adminURL+"/api/auth/login",
		bytes.NewBufferString(`{"user_name":"admin","password":"admin"}`),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, fmt.Errorf("observability login returned %s: %s", response.Status, detail)
	}
	return client, nil
}

func smokeGetJSON(ctx context.Context, client *http.Client, url string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %s", url, response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}
