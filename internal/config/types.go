package config

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const RedactionMarker = "__REDACTED__"

type ServerConfig struct {
	AdminAddr        string              `json:"admin_addr"`
	ServerAddr       string              `json:"server_addr"`
	TrustedProxies   []string            `json:"trusted_proxy_cidrs"`
	Users            []User              `json:"users"`
	ClientConfigFile string              `json:"client_config_file"`
	DataDB           string              `json:"data_db"`
	TargetPolicy     TargetPolicyConfig  `json:"target_policy"`
	DataPlane        DataPlaneConfig     `json:"data_plane"`
	Timeouts         TimeoutConfig       `json:"timeouts"`
	Limits           LimitConfig         `json:"limits"`
	Observability    ObservabilityConfig `json:"observability"`
}

type User struct {
	UserName     string `json:"user_name"`
	PasswordHash string `json:"password_hash"`
}

type DataPlaneConfig struct {
	DefaultTransport string     `json:"default_transport"`
	DirectFallback   bool       `json:"direct_fallback"`
	SMux             SMuxConfig `json:"smux"`
	UDP              UDPConfig  `json:"udp"`
}

type SMuxConfig struct {
	ProtocolVersion      int    `json:"protocol_version"`
	MinSessions          int    `json:"min_sessions"`
	MaxSessions          int    `json:"max_sessions"`
	MaxStreamsPerSession int    `json:"max_streams_per_session"`
	ScaleUpStreams       int    `json:"scale_up_streams"`
	SessionMaxAge        string `json:"session_max_age"`
	DrainTimeout         string `json:"drain_timeout"`
	KeepAliveInterval    string `json:"keepalive_interval"`
	KeepAliveTimeout     string `json:"keepalive_timeout"`
	MaxFrameSize         int    `json:"max_frame_size"`
	MaxReceiveBuffer     int    `json:"max_receive_buffer"`
	MaxStreamBuffer      int    `json:"max_stream_buffer"`
}

type UDPConfig struct {
	ChannelsPerProxy      int    `json:"channels_per_proxy"`
	FlowIdleTimeout       string `json:"flow_idle_timeout"`
	MaxFlowsPerProxy      int    `json:"max_flows_per_proxy"`
	MaxDatagramSize       int    `json:"max_datagram_size"`
	ChannelQueueDatagrams int    `json:"channel_queue_datagrams"`
}

type TimeoutConfig struct {
	ControlHandshake  string `json:"control_handshake"`
	HeartbeatInterval string `json:"heartbeat_interval"`
	HeartbeatTimeout  string `json:"heartbeat_timeout"`
	TargetDial        string `json:"target_dial"`
	DataConnect       string `json:"data_connect"`
	TCPIdle           string `json:"tcp_idle"`
	TCPMaxLifetime    string `json:"tcp_max_lifetime"`
}

type LimitConfig struct {
	MaxPendingTunnels              int `json:"max_pending_tunnels"`
	MaxTunnelsPerClient            int `json:"max_tunnels_per_client"`
	MaxControlMessageBytes         int `json:"max_control_message_bytes"`
	MaxDataHandshakesPerMinute     int `json:"max_data_handshakes_per_minute"`
	MaxAdminLoginAttemptsPerMinute int `json:"max_admin_login_attempts_per_minute"`
	MaxMuxSessionsTotal            int `json:"max_mux_sessions_total"`
	MaxUDPFlowsTotal               int `json:"max_udp_flows_total"`
}

type ObservabilityConfig struct {
	AuditRetention             string `json:"audit_retention"`
	NetworkRetention           string `json:"network_retention"`
	NetworkSampleRetention     string `json:"network_sample_retention"`
	NetworkStatisticsRetention string `json:"network_statistics_retention"`
	NetworkFineRollupRetention string `json:"network_fine_rollup_retention"`
	GeoIPCountryDatabase       string `json:"geoip_country_database"`
	GeoIPReloadInterval        string `json:"geoip_reload_interval"`
	SampleInterval             string `json:"sample_interval"`
	CleanupInterval            string `json:"cleanup_interval"`
	MaxActiveConnections       int    `json:"max_active_connections"`
}

type ClientFile struct {
	Revision     uint64         `json:"revision"`
	ClientConfig []ClientConfig `json:"client_config"`
}

type ClientConfig struct {
	ClientID    string          `json:"client_id"`
	ClientToken string          `json:"client_token"`
	Remark      string          `json:"remark"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
	ProxyList   []ProxyEnvelope `json:"proxy_list"`
}

type ProxyEnvelope struct {
	ProxyID string          `json:"proxy_id"`
	Remark  string          `json:"remark"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Data    json.RawMessage `json:"data"`
}

type TunnelData struct {
	Network           string `json:"network"`
	Transport         string `json:"transport"`
	Bind              string `json:"bind"`
	Connect           string `json:"connect"`
	TransportExplicit bool   `json:"-"`
}

type ClientRuntimeConfig struct {
	Server      string `json:"server"`
	EnableTLS   bool   `json:"enable_tls"`
	TLSCAFile   string `json:"tls_ca_file"`
	Path        string `json:"path"`
	ClientID    string `json:"client_id"`
	ClientToken string `json:"client_token"`
}

func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		AdminAddr:      "127.0.0.1:5100",
		ServerAddr:     "127.0.0.1:6100",
		TrustedProxies: []string{},
		DataPlane: DataPlaneConfig{
			DefaultTransport: "auto",
			DirectFallback:   true,
			SMux: SMuxConfig{
				ProtocolVersion: 2, MinSessions: 1, MaxSessions: 4,
				MaxStreamsPerSession: 128, ScaleUpStreams: 96,
				SessionMaxAge: "1h", DrainTimeout: "5m",
				KeepAliveInterval: "20s", KeepAliveTimeout: "60s",
				MaxFrameSize: 32768, MaxReceiveBuffer: 4194304,
				MaxStreamBuffer: 524288,
			},
			UDP: UDPConfig{
				ChannelsPerProxy: 2, FlowIdleTimeout: "60s",
				MaxFlowsPerProxy: 4096, MaxDatagramSize: 65507,
				ChannelQueueDatagrams: 1024,
			},
		},
		Timeouts: TimeoutConfig{
			ControlHandshake: "10s", HeartbeatInterval: "20s",
			HeartbeatTimeout: "60s", TargetDial: "10s",
			DataConnect: "15s", TCPIdle: "10m",
			TCPMaxLifetime: "24h",
		},
		Limits: LimitConfig{
			MaxPendingTunnels: 1024, MaxTunnelsPerClient: 256,
			MaxDataHandshakesPerMinute:     60000,
			MaxAdminLoginAttemptsPerMinute: 10,
			MaxControlMessageBytes:         65536, MaxMuxSessionsTotal: 4096,
			MaxUDPFlowsTotal: 65536,
		},
		Observability: ObservabilityConfig{
			AuditRetention: "4320h", NetworkRetention: "720h",
			NetworkSampleRetention: "168h", NetworkStatisticsRetention: "2160h",
			NetworkFineRollupRetention: "48h", GeoIPReloadInterval: "1h",
			SampleInterval: "10s", CleanupInterval: "1h", MaxActiveConnections: 100000,
		},
	}
}

func LoadServer(path string) (ServerConfig, error) {
	cfg := DefaultServerConfig()
	if err := decodeStrictFile(path, &cfg); err != nil {
		return ServerConfig{}, err
	}
	base := filepath.Dir(path)
	cfg.ClientConfigFile = resolvePath(base, cfg.ClientConfigFile)
	cfg.DataDB = resolvePath(base, cfg.DataDB)
	if cfg.Observability.GeoIPCountryDatabase != "" {
		cfg.Observability.GeoIPCountryDatabase = resolvePath(
			base,
			cfg.Observability.GeoIPCountryDatabase,
		)
	}
	if err := cfg.Validate(); err != nil {
		return ServerConfig{}, fmt.Errorf("validate server config: %w", err)
	}
	return cfg, nil
}

func LoadClientFile(path string) (ClientFile, error) {
	var cfg ClientFile
	if err := decodeStrictFile(path, &cfg); err != nil {
		return ClientFile{}, err
	}
	if err := cfg.Validate("", ""); err != nil {
		return ClientFile{}, fmt.Errorf("validate client config file: %w", err)
	}
	return cfg, nil
}

func LoadClient(path string) (ClientRuntimeConfig, error) {
	var cfg ClientRuntimeConfig
	if err := decodeStrictFile(path, &cfg); err != nil {
		return ClientRuntimeConfig{}, err
	}
	if cfg.Path == "" {
		cfg.Path = "/portpilot"
	}
	cfg.Path = "/" + strings.Trim(cfg.Path, "/")
	if cfg.Server == "" || cfg.ClientID == "" {
		return ClientRuntimeConfig{}, errors.New("server and client_id are required")
	}
	if err := ValidateToken(cfg.ClientToken); err != nil {
		return ClientRuntimeConfig{}, err
	}
	if _, _, err := net.SplitHostPort(cfg.Server); err != nil {
		return ClientRuntimeConfig{}, fmt.Errorf("invalid server: %w", err)
	}
	if cfg.TLSCAFile != "" {
		cfg.TLSCAFile = resolvePath(filepath.Dir(path), cfg.TLSCAFile)
	}
	return cfg, nil
}

func (c ServerConfig) Validate() error {
	if err := validateListenAddress("admin_addr", c.AdminAddr); err != nil {
		return err
	}
	if err := validateListenAddress("server_addr", c.ServerAddr); err != nil {
		return err
	}
	if samePort(c.AdminAddr, c.ServerAddr) {
		return errors.New("admin_addr and server_addr ports must differ")
	}
	if c.ClientConfigFile == "" || c.DataDB == "" {
		return errors.New("client_config_file and data_db are required")
	}
	if err := validateUsers(c.Users); err != nil {
		return err
	}
	if err := validateTrustedProxies(c.TrustedProxies); err != nil {
		return err
	}
	if err := c.TargetPolicy.Validate(); err != nil {
		return err
	}
	if err := validateDataPlane(c.DataPlane); err != nil {
		return err
	}
	if err := validateTimeouts(c); err != nil {
		return err
	}
	if err := validateLimits(c.Limits); err != nil {
		return err
	}
	return validateObservability(c.Observability)
}

func validateUsers(users []User) error {
	if len(users) == 0 {
		return errors.New("at least one admin user is required")
	}
	seen := make(map[string]struct{}, len(users))
	for _, user := range users {
		if user.UserName == "" || user.PasswordHash == "" {
			return errors.New("admin user name and password_hash are required")
		}
		if _, ok := seen[user.UserName]; ok {
			return fmt.Errorf("duplicate admin user %q", user.UserName)
		}
		cost, err := bcrypt.Cost([]byte(user.PasswordHash))
		if err != nil || cost < bcrypt.DefaultCost {
			return fmt.Errorf("admin user %q password_hash must be bcrypt cost %d or greater", user.UserName, bcrypt.DefaultCost)
		}
		seen[user.UserName] = struct{}{}
	}
	return nil
}

func validateTrustedProxies(cidrs []string) error {
	for _, cidr := range cidrs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid trusted proxy %q: %w", cidr, err)
		}
	}
	return nil
}

func validateDataPlane(dataPlane DataPlaneConfig) error {
	if dataPlane.DefaultTransport != "auto" && dataPlane.DefaultTransport != "smux" && dataPlane.DefaultTransport != "direct" {
		return errors.New("default_transport must be auto, smux, or direct")
	}
	smuxConfig := dataPlane.SMux
	if smuxConfig.ProtocolVersion != 2 || smuxConfig.MinSessions < 1 || smuxConfig.MaxSessions < smuxConfig.MinSessions ||
		smuxConfig.MaxStreamsPerSession < 1 || smuxConfig.ScaleUpStreams < 1 ||
		smuxConfig.ScaleUpStreams > smuxConfig.MaxStreamsPerSession {
		return errors.New("invalid smux limits")
	}
	if smuxConfig.MaxFrameSize < 1024 || smuxConfig.MaxReceiveBuffer < smuxConfig.MaxFrameSize ||
		smuxConfig.MaxStreamBuffer < smuxConfig.MaxFrameSize {
		return errors.New("invalid smux buffer limits")
	}
	udpConfig := dataPlane.UDP
	if udpConfig.ChannelsPerProxy < 1 || udpConfig.MaxFlowsPerProxy < 1 ||
		udpConfig.MaxDatagramSize < 1 || udpConfig.MaxDatagramSize > 65507 ||
		udpConfig.ChannelQueueDatagrams < 1 {
		return errors.New("invalid udp limits")
	}
	return nil
}

func validateTimeouts(c ServerConfig) error {
	values := map[string]string{
		"session_max_age": c.DataPlane.SMux.SessionMaxAge, "drain_timeout": c.DataPlane.SMux.DrainTimeout,
		"keepalive_interval": c.DataPlane.SMux.KeepAliveInterval, "keepalive_timeout": c.DataPlane.SMux.KeepAliveTimeout,
		"flow_idle_timeout": c.DataPlane.UDP.FlowIdleTimeout, "control_handshake": c.Timeouts.ControlHandshake,
		"heartbeat_interval": c.Timeouts.HeartbeatInterval, "heartbeat_timeout": c.Timeouts.HeartbeatTimeout,
		"target_dial": c.Timeouts.TargetDial, "data_connect": c.Timeouts.DataConnect, "tcp_idle": c.Timeouts.TCPIdle,
		"tcp_max_lifetime": c.Timeouts.TCPMaxLifetime,
	}
	for name, value := range values {
		if _, err := positiveDuration(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func validateLimits(limits LimitConfig) error {
	if limits.MaxPendingTunnels < 1 || limits.MaxTunnelsPerClient < 1 ||
		limits.MaxControlMessageBytes < 1024 || limits.MaxDataHandshakesPerMinute < 1 ||
		limits.MaxAdminLoginAttemptsPerMinute < 1 ||
		limits.MaxMuxSessionsTotal < 1 || limits.MaxUDPFlowsTotal < 1 {
		return errors.New("invalid resource limits")
	}
	return nil
}

func validateObservability(value ObservabilityConfig) error {
	if err := validateObservabilityRetention(value); err != nil {
		return err
	}
	return validateObservabilityRuntime(value)
}

func validateObservabilityRetention(value ObservabilityConfig) error {
	audit, err := positiveDuration(value.AuditRetention)
	if err != nil || audit < 30*24*time.Hour || audit > 730*24*time.Hour {
		return errors.New("audit_retention must be between 30d and 730d")
	}
	network, err := positiveDuration(value.NetworkRetention)
	if err != nil || network < 24*time.Hour || network > 365*24*time.Hour {
		return errors.New("network_retention must be between 1d and 365d")
	}
	samples, err := positiveDuration(value.NetworkSampleRetention)
	if err != nil || samples < 24*time.Hour || samples > 30*24*time.Hour || samples > network {
		return errors.New("network_sample_retention must be between 1d and 30d and not exceed network_retention")
	}
	statistics, err := positiveDuration(value.NetworkStatisticsRetention)
	if err != nil || statistics < 30*24*time.Hour || statistics > 365*24*time.Hour {
		return errors.New("network_statistics_retention must be between 30d and 365d")
	}
	fineRollups, err := positiveDuration(value.NetworkFineRollupRetention)
	if err != nil || fineRollups < 24*time.Hour || fineRollups > 7*24*time.Hour || fineRollups > statistics {
		return errors.New("network_fine_rollup_retention must be between 1d and 7d and not exceed network_statistics_retention")
	}
	return nil
}

func validateObservabilityRuntime(value ObservabilityConfig) error {
	geoIPReload, err := positiveDuration(value.GeoIPReloadInterval)
	if err != nil || geoIPReload < 10*time.Minute || geoIPReload > 24*time.Hour {
		return errors.New("geoip_reload_interval must be between 10m and 24h")
	}
	sampleInterval, err := positiveDuration(value.SampleInterval)
	if err != nil || sampleInterval < 5*time.Second || sampleInterval > time.Minute {
		return errors.New("sample_interval must be between 5s and 60s")
	}
	cleanupInterval, err := positiveDuration(value.CleanupInterval)
	if err != nil || cleanupInterval < 10*time.Minute || cleanupInterval > 24*time.Hour {
		return errors.New("cleanup_interval must be between 10m and 24h")
	}
	if value.MaxActiveConnections < 1000 || value.MaxActiveConnections > 1000000 {
		return errors.New("max_active_connections must be between 1000 and 1000000")
	}
	return nil
}

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (c ClientFile) Validate(adminAddr, serverAddr string) error {
	return c.validate(adminAddr, serverAddr, true)
}

func (c ClientFile) ValidateStrict(adminAddr, serverAddr string) error {
	return c.validate(adminAddr, serverAddr, false)
}

func (c ClientFile) validate(adminAddr, serverAddr string, allowLegacyMetadata bool) error {
	clientIDs := map[string]struct{}{}
	proxyIDs := map[string]struct{}{}
	ports := reservedPorts(adminAddr, serverAddr)
	for _, client := range c.ClientConfig {
		if err := validateClientIdentity(client, clientIDs, allowLegacyMetadata); err != nil {
			return err
		}
		for _, proxy := range client.ProxyList {
			if err := validateProxyBinding(proxy, proxyIDs, ports); err != nil {
				return err
			}
		}
	}
	return nil
}

func reservedPorts(addresses ...string) map[int]string {
	ports := map[int]string{}
	for _, address := range addresses {
		if address == "" {
			continue
		}
		_, port, err := net.SplitHostPort(address)
		if err == nil {
			value, _ := strconv.Atoi(port)
			ports[value] = "reserved service port"
		}
	}
	return ports
}

func validateClientIdentity(
	client ClientConfig,
	clientIDs map[string]struct{},
	allowLegacyMetadata bool,
) error {
	if len(client.ClientID) > 64 || !clientIDPattern.MatchString(client.ClientID) {
		return fmt.Errorf("invalid client_id %q", client.ClientID)
	}
	if _, ok := clientIDs[client.ClientID]; ok {
		return fmt.Errorf("duplicate client_id %q", client.ClientID)
	}
	clientIDs[client.ClientID] = struct{}{}
	if err := ValidateToken(client.ClientToken); err != nil {
		return fmt.Errorf("client %s: %w", client.ClientID, err)
	}
	remark := strings.TrimSpace(client.Remark)
	legacyMetadata := remark == "" || client.CreatedAt == 0 || client.UpdatedAt == 0
	if !allowLegacyMetadata || !legacyMetadata {
		if remark == "" || utf8.RuneCountInString(remark) > 200 {
			return fmt.Errorf("client %s: remark must contain 1 to 200 characters", client.ClientID)
		}
		if client.CreatedAt <= 0 || client.UpdatedAt <= 0 || client.UpdatedAt < client.CreatedAt {
			return fmt.Errorf("client %s: invalid created_at/updated_at Unix milliseconds", client.ClientID)
		}
		return nil
	}
	if remark != "" && utf8.RuneCountInString(remark) > 200 {
		return fmt.Errorf("client %s: remark must not exceed 200 characters", client.ClientID)
	}
	if client.CreatedAt < 0 || client.UpdatedAt < 0 ||
		(client.CreatedAt > 0 && client.UpdatedAt > 0 && client.UpdatedAt < client.CreatedAt) {
		return fmt.Errorf("client %s: invalid legacy created_at/updated_at values", client.ClientID)
	}
	return nil
}

func validateProxyBinding(
	proxy ProxyEnvelope,
	proxyIDs map[string]struct{},
	ports map[int]string,
) error {
	if proxy.ProxyID == "" {
		return errors.New("proxy_id is required")
	}
	if _, ok := proxyIDs[proxy.ProxyID]; ok {
		return fmt.Errorf("duplicate proxy_id %q", proxy.ProxyID)
	}
	proxyIDs[proxy.ProxyID] = struct{}{}
	if proxy.Kind != "tunnel" {
		return fmt.Errorf("proxy %s: unsupported kind %q", proxy.ProxyID, proxy.Kind)
	}
	tunnel, err := proxy.Tunnel()
	if err != nil {
		return fmt.Errorf("proxy %s: %w", proxy.ProxyID, err)
	}
	_, bindPort, err := net.SplitHostPort(tunnel.Bind)
	if err != nil {
		return fmt.Errorf("proxy %s invalid bind: %w", proxy.ProxyID, err)
	}
	port, _ := strconv.Atoi(bindPort)
	if previous, ok := ports[port]; ok {
		return fmt.Errorf("proxy %s port %d conflicts with %s", proxy.ProxyID, port, previous)
	}
	ports[port] = proxy.ProxyID
	return nil
}

func (p ProxyEnvelope) Tunnel() (TunnelData, error) {
	var data TunnelData
	if err := decodeStrictBytes(p.Data, &data); err != nil {
		return TunnelData{}, err
	}
	if data.Network != "tcp" && data.Network != "udp" {
		return TunnelData{}, errors.New("network must be tcp or udp")
	}
	if _, _, err := net.SplitHostPort(data.Bind); err != nil {
		return TunnelData{}, fmt.Errorf("invalid bind: %w", err)
	}
	if _, _, err := net.SplitHostPort(data.Connect); err != nil {
		return TunnelData{}, fmt.Errorf("invalid connect: %w", err)
	}
	data.TransportExplicit = data.Transport != ""
	if data.Network == "udp" {
		if data.Transport == "" {
			data.Transport = "smux-datagram"
		}
		if data.Transport != "smux-datagram" {
			return TunnelData{}, errors.New("udp transport must be smux-datagram")
		}
	} else {
		if data.Transport == "" {
			data.Transport = "auto"
		}
		if data.Transport != "auto" && data.Transport != "smux" && data.Transport != "direct" {
			return TunnelData{}, errors.New("tcp transport must be auto, smux, or direct")
		}
	}
	return data, nil
}

func (c ServerConfig) Duration(value string) time.Duration {
	d, _ := time.ParseDuration(value)
	return d
}

func ValidateToken(token string) error {
	raw := strings.TrimSpace(token)
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) >= 32 {
		return nil
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(raw); err == nil && len(decoded) >= 32 {
			return nil
		}
	}
	return errors.New("client_token must be base64 or hex with at least 32 decoded bytes")
}

func decodeStrictFile(path string, target any) error {
	file, err := os.Open(path) //nolint:gosec // The path is an explicit operator-supplied configuration file.
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode %s: trailing JSON values", path)
	}
	return nil
}

func decodeStrictBytes(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON values")
	}
	return nil
}

func resolvePath(base, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(base, value))
}

func validateListenAddress(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid %s port", name)
	}
	return nil
}

func samePort(a, b string) bool {
	_, pa, _ := net.SplitHostPort(a)
	_, pb, _ := net.SplitHostPort(b)
	return pa != "" && pa == pb
}

func positiveDuration(value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, errors.New("must be a positive duration")
	}
	return d, nil
}
