package accesspolicy

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/portpilot/portpilot/internal/config"
)

type Policy struct {
	privateOnly bool
	allow       []netip.Prefix
	deny        []netip.Prefix
}

func Compile(value config.AccessPolicyConfig) Policy {
	return Policy{
		privateOnly: value.PrivateOnly,
		allow:       parsePrefixes(value.AllowCIDRs),
		deny:        parsePrefixes(value.DenyCIDRs),
	}
}

func parsePrefixes(values []string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err == nil {
			result = append(result, prefix)
		}
	}
	return result
}

func (p Policy) Decide(address netip.Addr) (bool, string) {
	if p.privateOnly && !address.IsLoopback() && !address.IsPrivate() {
		return false, "private_only"
	}
	for _, prefix := range p.deny {
		if prefix.Contains(address) {
			return false, "deny"
		}
	}
	if len(p.allow) == 0 {
		return true, ""
	}
	for _, prefix := range p.allow {
		if prefix.Contains(address) {
			return true, ""
		}
	}
	return false, "allow_miss"
}

func NormalizeAddr(value net.Addr) (netip.Addr, error) {
	switch address := value.(type) {
	case *net.TCPAddr:
		return normalizeIP(address.IP)
	case *net.UDPAddr:
		return normalizeIP(address.IP)
	}
	host, _, err := net.SplitHostPort(value.String())
	if err != nil {
		return netip.Addr{}, fmt.Errorf("split remote address: %w", err)
	}
	if index := zoneIndex(host); index >= 0 {
		host = host[:index]
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse remote IP: %w", err)
	}
	return address.Unmap(), nil
}

func NormalizeIPString(value string) (netip.Addr, error) {
	if index := zoneIndex(value); index >= 0 {
		value = value[:index]
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse IP: %w", err)
	}
	return address.Unmap(), nil
}

func normalizeIP(value net.IP) (netip.Addr, error) {
	address, ok := netip.AddrFromSlice(value)
	if !ok {
		return netip.Addr{}, errorsInvalidIP
	}
	return address.Unmap(), nil
}

func zoneIndex(value string) int {
	for index := range len(value) {
		if value[index] == '%' {
			return index
		}
	}
	return -1
}

var errorsInvalidIP = fmt.Errorf("invalid IP address")
