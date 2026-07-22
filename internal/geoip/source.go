package geoip

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

type SourceType string

const (
	SourceDisabled SourceType = "disabled"
	SourceLocal    SourceType = "local"
	SourceRemote   SourceType = "remote"
)

type Source struct {
	Type   SourceType
	Local  string
	Remote *url.URL
	hash   string
}

func ParseSource(value string) (Source, error) {
	if value == "" {
		return Source{Type: SourceDisabled}, nil
	}
	if strings.TrimSpace(value) != value {
		return Source{}, errors.New("geoip source has surrounding whitespace")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return Source{}, errors.New("geoip source must be a local path or HTTPS URL")
	}
	if parsed.Scheme == "" {
		return Source{Type: SourceLocal, Local: value}, nil
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return Source{}, errors.New("remote geoip source must use HTTPS without userinfo or fragment")
	}
	sum := sha256.Sum256([]byte(parsed.String()))
	return Source{Type: SourceRemote, Remote: parsed, hash: hex.EncodeToString(sum[:])}, nil
}

func (s Source) Hash() string {
	return s.hash
}

func (s Source) Redacted() string {
	if s.Type != SourceRemote || s.Remote == nil {
		return string(s.Type)
	}
	copy := *s.Remote
	if copy.RawQuery != "" {
		copy.RawQuery = "redacted"
		copy.ForceQuery = true
	}
	return copy.String()
}
