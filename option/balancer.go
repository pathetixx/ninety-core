package option

type BalancerOutboundOptions struct {
	Outbounds                 []string `json:"outbounds"`
	Strategy                  string   `json:"strategy,omitempty"`
	Tolerance                 uint16   `json:"tolerance,omitempty"`
	InterruptExistConnections bool     `json:"interrupt_exist_connections,omitempty"`
}
