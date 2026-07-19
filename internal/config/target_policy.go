package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

type TargetPolicyConfig struct {
	AllowCIDRs []string `json:"allow_cidrs"`
	DenyCIDRs  []string `json:"deny_cidrs"`
	AllowPorts []int    `json:"allow_ports"`
	DenyPorts  []int    `json:"deny_ports"`
}

func (p TargetPolicyConfig) Validate() error {
	for _, item := range append(append([]string(nil), p.AllowCIDRs...), p.DenyCIDRs...) {
		if _, _, err := net.ParseCIDR(item); err != nil {
			return fmt.Errorf("invalid target policy CIDR %q: %w", item, err)
		}
	}
	for _, port := range append(append([]int(nil), p.AllowPorts...), p.DenyPorts...) {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid target policy port %d", port)
		}
	}
	return nil
}

func (p TargetPolicyConfig) Allows(connect string) error {
	host, rawPort, err := net.SplitHostPort(connect)
	if err != nil {
		return fmt.Errorf("invalid target: %w", err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		return errors.New("invalid target port")
	}
	if containsPort(p.DenyPorts, port) {
		return fmt.Errorf("target port %d is denied", port)
	}
	if len(p.AllowPorts) > 0 && !containsPort(p.AllowPorts, port) {
		return fmt.Errorf("target port %d is not allowed", port)
	}
	if len(p.AllowCIDRs) == 0 && len(p.DenyCIDRs) == 0 {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("target must use an IP literal when CIDR policy is enabled")
	}
	if containsIP(p.DenyCIDRs, ip) {
		return fmt.Errorf("target address %s is denied", host)
	}
	if len(p.AllowCIDRs) > 0 && !containsIP(p.AllowCIDRs, ip) {
		return fmt.Errorf("target address %s is not allowed", host)
	}
	return nil
}

func containsPort(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsIP(cidrs []string, ip net.IP) bool {
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(raw)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func ValidateTargets(file ClientFile, policy TargetPolicyConfig) error {
	for _, client := range file.ClientConfig {
		for _, proxy := range client.ProxyList {
			tunnel, err := proxy.Tunnel()
			if err != nil {
				return err
			}
			if err := policy.Allows(tunnel.Connect); err != nil {
				return fmt.Errorf("proxy %s target rejected: %w", proxy.ProxyID, err)
			}
		}
	}
	return nil
}
