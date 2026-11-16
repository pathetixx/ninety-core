package option

import "github.com/sagernet/sing/common/json/badoption"

// MieruOutboundOptions follows enfein/mbox, the mieru author's sing-box fork,
// plus the two profile fields a mierus:// link carries and mbox leaves out.
type MieruOutboundOptions struct {
	DialerOptions
	ServerOptions
	ServerPortRanges badoption.Listable[string] `json:"server_ports,omitempty"`
	Transport        string                     `json:"transport,omitempty"`
	UserName         string                     `json:"username,omitempty"`
	Password         string                     `json:"password,omitempty"`
	Multiplexing     string                     `json:"multiplexing,omitempty"`
	HandshakeMode    string                     `json:"handshake_mode,omitempty"`
	MTU              uint32                     `json:"mtu,omitempty"`
	TrafficPattern   string                     `json:"traffic_pattern,omitempty"`
}
