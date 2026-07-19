package client

import (
	"time"

	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/xtaci/smux"
)

func clientSMuxConfig(params protocol.SMuxParameters) *smux.Config {
	cfg := smux.DefaultConfig()
	if params.ProtocolVersion == 0 {
		params.ProtocolVersion = 2
	}
	cfg.Version = params.ProtocolVersion
	cfg.KeepAliveInterval = durationOr(params.KeepAliveInterval, 20*time.Second)
	cfg.KeepAliveTimeout = durationOr(params.KeepAliveTimeout, 60*time.Second)
	if params.MaxFrameSize > 0 {
		cfg.MaxFrameSize = params.MaxFrameSize
	}
	if params.MaxReceiveBuffer > 0 {
		cfg.MaxReceiveBuffer = params.MaxReceiveBuffer
	}
	if params.MaxStreamBuffer > 0 {
		cfg.MaxStreamBuffer = params.MaxStreamBuffer
	}
	return cfg
}

func durationOr(value string, fallback time.Duration) time.Duration {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fallback
	}
	return duration
}

func normalizedUDP(params protocol.UDPParameters) protocol.UDPParameters {
	if params.ChannelsPerProxy < 1 {
		params.ChannelsPerProxy = 1
	}
	if params.FlowIdleTimeout == "" {
		params.FlowIdleTimeout = "60s"
	}
	if params.MaxFlowsPerProxy < 1 {
		params.MaxFlowsPerProxy = 4096
	}
	if params.MaxDatagramSize < 1 {
		params.MaxDatagramSize = 65507
	}
	if params.ChannelQueueDatagrams < 1 {
		params.ChannelQueueDatagrams = 1024
	}
	return params
}
