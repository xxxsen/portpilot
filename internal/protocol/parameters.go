package protocol

type SMuxParameters struct {
	ProtocolVersion   int    `json:"protocol_version"`
	ScaleUpStreams    int    `json:"scale_up_streams"`
	SessionMaxAge     string `json:"session_max_age"`
	DrainTimeout      string `json:"drain_timeout"`
	KeepAliveInterval string `json:"keepalive_interval"`
	KeepAliveTimeout  string `json:"keepalive_timeout"`
	MaxFrameSize      int    `json:"max_frame_size"`
	MaxReceiveBuffer  int    `json:"max_receive_buffer"`
	MaxStreamBuffer   int    `json:"max_stream_buffer"`
}

type UDPParameters struct {
	ChannelsPerProxy      int    `json:"channels_per_proxy"`
	FlowIdleTimeout       string `json:"flow_idle_timeout"`
	MaxFlowsPerProxy      int    `json:"max_flows_per_proxy"`
	MaxDatagramSize       int    `json:"max_datagram_size"`
	ChannelQueueDatagrams int    `json:"channel_queue_datagrams"`
}
