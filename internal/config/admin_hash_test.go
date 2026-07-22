package config

import "testing"

func TestServerConfigRejectsInvalidAdminPasswordHash(t *testing.T) {
	t.Parallel()
	cfg := DefaultServerConfig()
	cfg.ClientConfigFile = "clients.json"
	cfg.Users = []User{{UserName: "admin", PasswordHash: "not-a-bcrypt-hash"}} //nolint:gosec // Intentionally invalid test fixture, not a credential.
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid administrator password hash was accepted")
	}
}
