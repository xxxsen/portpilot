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

type analyticsTopIP struct {
	SourceIP string `json:"source_ip"`
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
			summary.UDPChannels >= 2 && summary.UDPFlows == 0 &&
			tcpConnections["dev-tcp-echo"] && tcpConnections["dev-tcp-direct"] &&
			udpFlows >= 2 {
			return smokeAnalytics(ctx, client, adminURL)
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

func smokeAnalytics(ctx context.Context, client *http.Client, adminURL string) error {
	for {
		var overview struct {
			Connections int64 `json:"connections"`
			UniqueIPs   int64 `json:"unique_ips"`
			UpBytes     int64 `json:"up_bytes"`
			DownBytes   int64 `json:"down_bytes"`
			Series      []struct {
				BucketAt int64 `json:"bucket_at"`
			} `json:"series"`
			Completeness struct {
				Status string `json:"status"`
			} `json:"completeness"`
		}
		if err := smokeGetJSON(
			ctx,
			client,
			adminURL+"/api/network/analytics/overview?range=1h",
			&overview,
		); err != nil {
			return err
		}
		if overview.Connections >= 5 &&
			overview.UniqueIPs >= 2 &&
			overview.UpBytes > 0 &&
			overview.DownBytes > 0 &&
			len(overview.Series) == 12 &&
			overview.Completeness.Status == "ready" {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"network analytics did not converge: connections=%d unique_ips=%d up=%d down=%d points=%d status=%s",
				overview.Connections,
				overview.UniqueIPs,
				overview.UpBytes,
				overview.DownBytes,
				len(overview.Series),
				overview.Completeness.Status,
			)
		case <-time.After(100 * time.Millisecond):
		}
	}
	var dimensions struct {
		Items []struct {
			ProxyID string `json:"proxy_id"`
		} `json:"items"`
	}
	if err := smokeGetJSON(
		ctx,
		client,
		adminURL+"/api/network/analytics/dimensions?range=1h&page=1&page_size=25",
		&dimensions,
	); err != nil {
		return err
	}
	proxies := make(map[string]bool)
	for _, item := range dimensions.Items {
		proxies[item.ProxyID] = true
	}
	if !proxies["dev-tcp-echo"] || !proxies["dev-tcp-direct"] || !proxies["dev-udp-echo"] {
		return fmt.Errorf("analytics dimensions missing proxy: %v", proxies)
	}
	var top struct {
		ByConnections []analyticsTopIP `json:"by_connections"`
		ByTraffic     []analyticsTopIP `json:"by_traffic"`
	}
	if err := smokeGetJSON(
		ctx,
		client,
		adminURL+"/api/network/analytics/top-ips?range=1h&k=10",
		&top,
	); err != nil {
		return err
	}
	if !sourceIPPrecedes(top.ByConnections, "127.0.0.2", "127.0.0.3") ||
		!sourceIPPrecedes(top.ByTraffic, "127.0.0.3", "127.0.0.2") {
		return fmt.Errorf(
			"analytics Top IP order mismatch: connections=%#v traffic=%#v",
			top.ByConnections,
			top.ByTraffic,
		)
	}
	var series struct {
		SourceIP    string `json:"source_ip"`
		Connections int64  `json:"connections"`
		Series      []struct {
			BucketAt int64 `json:"bucket_at"`
		} `json:"series"`
	}
	if err := smokeGetJSON(
		ctx,
		client,
		adminURL+"/api/network/analytics/ip-series?range=1h&source_ip=127.0.0.2",
		&series,
	); err != nil {
		return err
	}
	if series.SourceIP != "127.0.0.2" || series.Connections < 3 || len(series.Series) != 12 {
		return fmt.Errorf("analytics IP series mismatch: %#v", series)
	}
	return nil
}

func sourceIPPrecedes(items []analyticsTopIP, first, second string) bool {
	firstIndex := -1
	secondIndex := -1
	for index, item := range items {
		switch item.SourceIP {
		case first:
			firstIndex = index
		case second:
			secondIndex = index
		}
	}
	return firstIndex >= 0 && secondIndex >= 0 && firstIndex < secondIndex
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
