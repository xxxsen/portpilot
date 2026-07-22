package geoip

import (
	"strings"
	"testing"
)

func TestParseSourceAndRedaction(t *testing.T) {
	for _, test := range []struct {
		value string
		kind  SourceType
		ok    bool
	}{
		{value: "", kind: SourceDisabled, ok: true},
		{value: "/tmp/country.mmdb", kind: SourceLocal, ok: true},
		{value: "https://example.test/country.tar.gz?license_key=secret", kind: SourceRemote, ok: true},
		{value: "http://example.test/country.mmdb", ok: false},
		{value: "https://user:" + "pass@example.test/country.mmdb", ok: false},
		{value: "https://example.test/country.mmdb#part", ok: false},
	} {
		source, err := ParseSource(test.value)
		if (err == nil) != test.ok {
			t.Fatalf("ParseSource(%q) error=%v", test.value, err)
		}
		if err == nil && source.Type != test.kind {
			t.Fatalf("ParseSource(%q) kind=%q", test.value, source.Type)
		}
		if source.Type == SourceRemote {
			if source.Hash() == "" || strings.Contains(source.Redacted(), "secret") {
				t.Fatalf("source credentials leaked: hash=%q redacted=%q", source.Hash(), source.Redacted())
			}
		}
	}
}
