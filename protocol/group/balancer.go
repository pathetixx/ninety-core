package group

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

func RegisterBalancer(registry *outbound.Registry) {
	outbound.Register[option.BalancerOutboundOptions](registry, C.TypeBalancer, NewBalancer)
}

var (
	_ adapter.OutboundGroup             = (*Balancer)(nil)
	_ adapter.ConnectionHandlerEx       = (*Balancer)(nil)
	_ adapter.PacketConnectionHandlerEx = (*Balancer)(nil)
)

// StrategyLowestDelay always routes through the outbound with the lowest
// measured delay. It is the only strategy implemented: the others in the
// upstream this was modelled on were never used by Ninety, and an unused
// strategy is an untested one.
const StrategyLowestDelay = "lowest-delay"

// timeoutDelay is the delay assigned to an outbound with no usable measurement,
// so it sorts behind every outbound that has one.
const timeoutDelay uint16 = 65535

// leaderPollInterval is how often the leader is re-evaluated. The delays
// themselves come from whatever fills the URLTest history (a urltest group
// alongside this one); this only decides how quickly a change in them is acted
// on.
const leaderPollInterval = time.Second

// Balancer routes each new connection through the lowest-delay outbound of its
// group, and can interrupt existing connections when the leader changes.
//
// It does not measure anything itself: delays come from the shared URLTest
// history, which a urltest group over the same outbounds keeps up to date. That
// keeps one health check feeding both the UI and this group.
type Balancer struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	connection                   adapter.ConnectionManager
	logger                       logger.ContextLogger
	tags                         []string
	tolerance                    uint16
	history                      adapter.URLTestHistoryStorage
	outbounds                    map[string]adapter.Outbound
	ordered                      []adapter.Outbound
	leader                       common.TypedValue[adapter.Outbound]
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	close                        chan struct{}
}

func NewBalancer(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.BalancerOutboundOptions) (adapter.Outbound, error) {
	if options.Strategy != "" && options.Strategy != StrategyLowestDelay {
		return nil, E.New("unsupported balancer strategy: ", options.Strategy)
	}
	balancer := &Balancer{
		Adapter:                      outbound.NewAdapter(C.TypeBalancer, tag, nil, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		tolerance:                    options.Tolerance,
		outbounds:                    make(map[string]adapter.Outbound),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: options.InterruptExistConnections,
		close:                        make(chan struct{}),
	}
	if len(balancer.tags) == 0 {
		return nil, E.New("missing tags")
	}
	return balancer, nil
}

func (s *Balancer) Start() error {
	for i, tag := range s.tags {
		detour, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return E.New("outbound ", i, " not found: ", tag)
		}
		s.outbounds[tag] = detour
		s.ordered = append(s.ordered, detour)
	}
	if historyFromCtx := service.PtrFromContext[urltest.HistoryStorage](s.ctx); historyFromCtx != nil {
		s.history = historyFromCtx
	} else if clashServer := service.FromContext[adapter.ClashServer](s.ctx); clashServer != nil {
		s.history = clashServer.HistoryStorage()
	} else {
		s.history = urltest.NewHistoryStorage()
	}
	// Something has to be selected before the first measurement lands.
	s.leader.Store(s.ordered[0])
	return nil
}

func (s *Balancer) PostStart() error {
	s.updateLeader()
	go s.loop()
	return nil
}

func (s *Balancer) Close() error {
	select {
	case <-s.close:
	default:
		close(s.close)
	}
	return nil
}

func (s *Balancer) loop() {
	ticker := time.NewTicker(leaderPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.close:
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.updateLeader()
		}
	}
}

func (s *Balancer) delay(detour adapter.Outbound) uint16 {
	history := s.history.LoadURLTestHistory(RealTag(detour))
	if history == nil || history.Delay == 0 {
		return timeoutDelay
	}
	return history.Delay
}

// updateLeader picks the lowest-delay outbound and, if that is a different one,
// interrupts the connections still running over the previous leader.
func (s *Balancer) updateLeader() {
	var (
		best      adapter.Outbound
		bestDelay = timeoutDelay
	)
	for _, detour := range s.ordered {
		delay := s.delay(detour)
		if best == nil || delay < bestDelay {
			best, bestDelay = detour, delay
		}
	}
	if best == nil {
		return
	}
	current := s.leader.Load()
	if current == best {
		return
	}
	// Tolerance keeps the group from flapping between outbounds that measure
	// within noise of each other, since every switch tears down connections.
	if current != nil && s.tolerance > 0 && s.delay(current) < bestDelay+s.tolerance {
		return
	}
	s.leader.Store(best)
	s.logger.Info("balancer selected ", best.Tag(), " (", bestDelay, "ms)")
	s.interruptGroup.Interrupt(s.interruptExternalConnections)
}

// dropMeasurement forgets an outbound's delay after it failed to dial, so it
// sorts last until the next health check produces a fresh one. Without this the
// group would keep electing a leader that is measurably fast and actually dead.
func (s *Balancer) dropMeasurement(detour adapter.Outbound) {
	s.history.DeleteURLTestHistory(RealTag(detour))
	s.updateLeader()
}

func (s *Balancer) Network() []string {
	leader := s.leader.Load()
	if leader == nil {
		return []string{N.NetworkTCP, N.NetworkUDP}
	}
	return leader.Network()
}

func (s *Balancer) Now() string {
	leader := s.leader.Load()
	if leader == nil {
		return s.tags[0]
	}
	return leader.Tag()
}

func (s *Balancer) All() []string {
	return s.tags
}

func (s *Balancer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	leader := s.leader.Load()
	if !common.Contains(leader.Network(), network) {
		return nil, E.New("missing supported outbound")
	}
	conn, err := leader.DialContext(ctx, network, destination)
	if err != nil {
		s.logger.ErrorContext(ctx, err)
		s.dropMeasurement(leader)
		return nil, err
	}
	return s.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Balancer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	leader := s.leader.Load()
	if !common.Contains(leader.Network(), N.NetworkUDP) {
		return nil, E.New("missing supported outbound")
	}
	conn, err := leader.ListenPacket(ctx, destination)
	if err != nil {
		s.logger.ErrorContext(ctx, err)
		s.dropMeasurement(leader)
		return nil, err
	}
	return s.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Balancer) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *Balancer) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

func (s *Balancer) NewDirectRouteConnection(metadata adapter.InboundContext, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error) {
	leader := s.leader.Load()
	if !common.Contains(leader.Network(), metadata.Network) {
		return nil, E.New(metadata.Network, " is not supported by outbound: ", leader.Tag())
	}
	return leader.(adapter.DirectRouteOutbound).NewDirectRouteConnection(metadata, routeContext, timeout)
}
