package proxyproto

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

const MaxV1HeaderBytes = 107

func V1(source, destination net.Addr) ([]byte, error) {
	sourceAddr, err := addrPort(source)
	if err != nil {
		return nil, fmt.Errorf("source address: %w", err)
	}
	destinationAddr, err := addrPort(destination)
	if err != nil {
		return nil, fmt.Errorf("destination address: %w", err)
	}
	family := ""
	switch {
	case sourceAddr.Addr().Is4() && destinationAddr.Addr().Is4():
		family = "TCP4"
	case sourceAddr.Addr().Is6() && destinationAddr.Addr().Is6():
		family = "TCP6"
	default:
		return nil, errors.New("source and destination address families differ")
	}
	header := []byte(fmt.Sprintf(
		"PROXY %s %s %s %d %d\r\n",
		family,
		sourceAddr.Addr(),
		destinationAddr.Addr(),
		sourceAddr.Port(),
		destinationAddr.Port(),
	))
	if len(header) > MaxV1HeaderBytes {
		return nil, errors.New("PROXY protocol v1 header exceeds 107 bytes")
	}
	return header, nil
}

func addrPort(value net.Addr) (netip.AddrPort, error) {
	switch address := value.(type) {
	case *net.TCPAddr:
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok || address.Port < 0 || address.Port > 65535 {
			return netip.AddrPort{}, errors.New("invalid TCP address")
		}
		return netip.AddrPortFrom(ip.Unmap(), uint16(address.Port)), nil
	default:
		parsed, err := netip.ParseAddrPort(value.String())
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("parse address: %w", err)
		}
		return netip.AddrPortFrom(parsed.Addr().Unmap(), parsed.Port()), nil
	}
}
