package geoip

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

func TestNormalizeExternalIP(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "192.0.2.1:5000", want: "192.0.2.1", ok: true},
		{input: "[2001:db8::1]:443", want: "2001:db8::1", ok: true},
		{input: "[fe80::1%eth0]:443", want: "fe80::1", ok: true},
		{input: "::ffff:192.0.2.2", want: "192.0.2.2", ok: true},
		{input: "not-an-address", ok: false},
	} {
		ip, ok := NormalizeExternalIP(test.input)
		if ok != test.ok {
			t.Fatalf("NormalizeExternalIP(%q) ok=%v, want %v", test.input, ok, test.ok)
		}
		if ok && ip.String() != test.want {
			t.Fatalf("NormalizeExternalIP(%q)=%q, want %q", test.input, ip, test.want)
		}
	}
}

func TestResolverClassifiesSpecialAddressesWithoutDatabase(t *testing.T) {
	t.Parallel()
	resolver, err := New("", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolver.Close)
	for _, test := range []struct {
		ip   string
		kind string
	}{
		{ip: "10.0.0.1", kind: "private"},
		{ip: "127.0.0.1", kind: "private"},
		{ip: "169.254.1.1", kind: "reserved"},
		{ip: "224.0.0.1", kind: "reserved"},
		{ip: "0.0.0.0", kind: "reserved"},
		{ip: "ff02::1", kind: "reserved"},
		{ip: "192.0.2.1", kind: "reserved"},
		{ip: "2001:db8::1", kind: "reserved"},
		{ip: "8.8.8.8", kind: "unknown"},
	} {
		if result := resolver.Resolve(netip.MustParseAddr(test.ip)); result.Kind != test.kind {
			t.Fatalf("Resolve(%s) kind=%q, want %q", test.ip, result.Kind, test.kind)
		}
	}
	if status := resolver.Status(); status.Status != "disabled" {
		t.Fatalf("disabled resolver status=%#v", status)
	}
	if result := resolver.Resolve(netip.Addr{}); result.Kind != "invalid" {
		t.Fatalf("invalid address classification=%#v", result)
	}
}

func TestResolverRejectsMissingConfiguredDatabase(t *testing.T) {
	t.Parallel()
	_, err := New(filepath.Join(t.TempDir(), "missing.mmdb"), time.Hour, nil)
	if err == nil {
		t.Fatal("configured missing MMDB was accepted")
	}
}

func TestResolverCountryLookupAndHotReload(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "country.mmdb")
	writeCountryDatabase(t, path, "GB")
	resolver, err := New(path, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolver.Close)
	ip := netip.MustParseAddr("81.2.69.142")
	first := resolver.Resolve(ip)
	if first.Kind != "country" || first.CountryCode != "GB" ||
		first.DatabaseFingerprint == "" {
		t.Fatalf("unexpected initial lookup: %#v", first)
	}
	missing := resolver.Resolve(netip.MustParseAddr("8.8.8.8"))
	if missing.Kind != "unknown" ||
		missing.DatabaseFingerprint != first.DatabaseFingerprint {
		t.Fatalf("unexpected missing lookup: %#v", missing)
	}

	writeCountryDatabase(t, path, "US")
	if err := resolver.reload(false); err != nil {
		t.Fatal(err)
	}
	second := resolver.Resolve(ip)
	if second.CountryCode != "US" ||
		second.DatabaseFingerprint == first.DatabaseFingerprint {
		t.Fatalf("hot reload did not replace country data: first=%#v second=%#v", first, second)
	}

	if err := os.WriteFile(path, []byte("not-an-mmdb"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := resolver.reload(false); err == nil {
		t.Fatal("invalid hot reload unexpectedly succeeded")
	} else {
		resolver.setError(err)
	}
	if result := resolver.Resolve(ip); result.CountryCode != "US" {
		t.Fatalf("failed reload discarded the previous reader: %#v", result)
	}
	if status := resolver.Status(); status.Status != "degraded" {
		t.Fatalf("failed reload status=%#v", status)
	}
}

func TestResolverLookupsRemainValidDuringReload(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "country.mmdb")
	writeCountryDatabase(t, path, "GB")
	resolver, err := New(path, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolver.Close)
	ip := netip.MustParseAddr("81.2.69.142")
	stop := make(chan struct{})
	failures := make(chan Result, 1)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-stop:
					return
				default:
					result := resolver.Resolve(ip)
					if result.CountryCode != "GB" && result.CountryCode != "US" {
						select {
						case failures <- result:
						default:
						}
						return
					}
				}
			}
		}()
	}
	for index := range 10 {
		country := "GB"
		if index%2 == 0 {
			country = "US"
		}
		writeCountryDatabase(t, path, country)
		if err := resolver.reload(false); err != nil {
			close(stop)
			wait.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	wait.Wait()
	select {
	case result := <-failures:
		t.Fatalf("lookup observed an invalid reader state: %#v", result)
	default:
	}
}

func writeCountryDatabase(t *testing.T, path string, countryCode string) {
	t.Helper()
	writer, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: "PortPilot-Country-Test"})
	if err != nil {
		t.Fatal(err)
	}
	_, network, err := net.ParseCIDR("81.2.69.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Insert(network, mmdbtype.Map{
		"country": mmdbtype.Map{"iso_code": mmdbtype.String(countryCode)},
	}); err != nil {
		t.Fatal(err)
	}
	temporary := path + ".tmp"
	//nolint:gosec // The path is derived from t.TempDir and controlled by this test.
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteTo(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}
