package config

import (
	"fmt"
	"net/netip"
)

const (
	maxAccessPolicyCIDRs = 256
	minRateBytesPerSec   = 1024
	maxRateBytesPerSec   = int64(1 << 40)
)

type AccessPolicyConfig struct {
	PrivateOnly bool     `json:"private_only"`
	AllowCIDRs  []string `json:"allow_cidrs"`
	DenyCIDRs   []string `json:"deny_cidrs"`
}

type RateLimitConfig struct {
	UpBytesPerSecond   int64 `json:"up_bytes_per_second"`
	DownBytesPerSecond int64 `json:"down_bytes_per_second"`
}

func (p AccessPolicyConfig) Validate() error {
	if p.PrivateOnly && (len(p.AllowCIDRs) > 0 || len(p.DenyCIDRs) > 0) {
		return fmt.Errorf("private_only cannot be combined with allow_cidrs or deny_cidrs")
	}
	allow, err := validateCIDRList("allow_cidrs", p.AllowCIDRs)
	if err != nil {
		return err
	}
	deny, err := validateCIDRList("deny_cidrs", p.DenyCIDRs)
	if err != nil {
		return err
	}
	for cidr := range allow {
		if _, ok := deny[cidr]; ok {
			return fmt.Errorf("CIDR %q cannot appear in both allow_cidrs and deny_cidrs", cidr)
		}
	}
	return nil
}

func validateCIDRList(name string, values []string) (map[string]struct{}, error) {
	if len(values) > maxAccessPolicyCIDRs {
		return nil, fmt.Errorf("%s must not contain more than %d entries", name, maxAccessPolicyCIDRs)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("%s contains invalid CIDR %q: %w", name, value, err)
		}
		if prefix != prefix.Masked() || value != prefix.String() {
			return nil, fmt.Errorf("%s CIDR %q must be a canonical network prefix", name, value)
		}
		if _, ok := seen[value]; ok {
			return nil, fmt.Errorf("%s contains duplicate CIDR %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return seen, nil
}

func (r RateLimitConfig) Validate() error {
	if err := validateRate("up_bytes_per_second", r.UpBytesPerSecond); err != nil {
		return err
	}
	return validateRate("down_bytes_per_second", r.DownBytesPerSecond)
}

func validateRate(name string, value int64) error {
	if value == 0 {
		return nil
	}
	if value < minRateBytesPerSec || value > maxRateBytesPerSec {
		return fmt.Errorf("%s must be zero or between 1024 and %d", name, maxRateBytesPerSec)
	}
	return nil
}
