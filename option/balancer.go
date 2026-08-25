package option

import "github.com/sagernet/sing/common/json/badoption"

type BalancerOutboundOptions struct {
	Outbounds                 []string           `json:"outbounds"`
	Strategy                  string             `json:"strategy,omitempty"`
	Tolerance                 uint16             `json:"tolerance,omitempty"`
	InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
	URL                       string             `json:"url,omitempty"`
	Interval                  badoption.Duration `json:"interval,omitempty"`
	Concurrency               int                `json:"concurrency,omitempty"`
	FailureCooldown           badoption.Duration `json:"failure_cooldown,omitempty"`
}
