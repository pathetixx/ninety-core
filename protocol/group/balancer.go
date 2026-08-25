package group

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
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
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func RegisterBalancer(registry *outbound.Registry) {
	outbound.Register[option.BalancerOutboundOptions](registry, C.TypeBalancer, NewBalancer)
}

var (
	_ adapter.OutboundGroup             = (*Balancer)(nil)
	_ adapter.URLTestGroup              = (*Balancer)(nil)
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

// leaderPollInterval is how often the leader is re-evaluated from the measured
// delays. The health check below refreshes those delays on its own schedule.
const leaderPollInterval = time.Second

const (
	defaultCheckInterval   = 3 * time.Minute
	defaultConcurrency     = 16
	defaultFailureCooldown = 30 * time.Second
	// A node that keeps failing backs off exponentially, but never past this:
	// subscriptions recover, and a permanently sidelined node is a lost node.
	maxFailureCooldown = 10 * time.Minute
	// Per-probe budget. C.TCPTimeout (15s) is the dial budget for real traffic;
	// for a health check it only means a sweep over a few hundred dead nodes
	// takes minutes, and the group has nothing to elect until it finishes.
	probeTimeout = 6 * time.Second
)

// Balancer routes each new connection through the lowest-delay outbound of its
// group, and can interrupt existing connections when the leader changes.
//
// It runs its own health check over its members and publishes the results into
// the shared URLTest history, so the UI and this group always read the same
// numbers. Earlier it only consumed that history and relied on a urltest group
// sitting next to it to fill it — but a urltest group only starts its periodic
// check once traffic dials through the group itself, which never happens when
// the balancer is the one carrying the traffic. The delays then froze at
// whatever the single start-up sweep produced, and every dial failure deleted
// one more of them until nothing was left to compare and the group pinned
// itself to the first member for good.
type Balancer struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	connection                   adapter.ConnectionManager
	logger                       logger.ContextLogger
	tags                         []string
	tolerance                    uint16
	link                         string
	interval                     time.Duration
	concurrency                  int
	cooldown                     time.Duration
	history                      adapter.URLTestHistoryStorage
	outbounds                    map[string]adapter.Outbound
	ordered                      []adapter.Outbound
	leader                       common.TypedValue[adapter.Outbound]
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	pause                        pause.Manager
	checking                     atomic.Bool
	failAccess                   sync.Mutex
	failures                     map[string]failureState
	rotation                     atomic.Uint64
	close                        chan struct{}
}

// failureState is what an outbound earned by failing to carry traffic, as
// opposed to failing a probe: streak drives the backoff, until is when it may
// be elected again.
type failureState struct {
	streak uint
	until  time.Time
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
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		concurrency:                  options.Concurrency,
		cooldown:                     time.Duration(options.FailureCooldown),
		outbounds:                    make(map[string]adapter.Outbound),
		failures:                     make(map[string]failureState),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: options.InterruptExistConnections,
		close:                        make(chan struct{}),
	}
	if len(balancer.tags) == 0 {
		return nil, E.New("missing tags")
	}
	if balancer.interval <= 0 {
		balancer.interval = defaultCheckInterval
	}
	if balancer.concurrency <= 0 {
		balancer.concurrency = defaultConcurrency
	}
	if balancer.cooldown <= 0 {
		balancer.cooldown = defaultFailureCooldown
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
	s.pause = service.FromContext[pause.Manager](s.ctx)
	// Something has to be selected before the first measurement lands.
	s.leader.Store(s.ordered[0])
	return nil
}

func (s *Balancer) PostStart() error {
	s.updateLeader()
	go s.loop()
	go s.checkLoop()
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

// checkLoop keeps the measurements this group elects on from going stale. The
// first sweep runs immediately: until it lands there is nothing to compare and
// the group is stuck with whatever Start picked.
func (s *Balancer) checkLoop() {
	s.CheckOutbounds()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	var pauseCallback *list.Element[pause.Callback]
	if s.pause != nil {
		pauseCallback = pause.RegisterTicker(s.pause, ticker, s.interval, nil)
		defer s.pause.UnregisterCallback(pauseCallback)
	}
	for {
		select {
		case <-s.close:
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.CheckOutbounds()
		}
	}
}

// CheckOutbounds refreshes measurements that the interval has aged out.
func (s *Balancer) CheckOutbounds() {
	_, _ = s.checkOutbounds(s.ctx, false)
}

// URLTest is what the Clash API calls for a group probe; it re-measures every
// member regardless of how fresh the last result is.
func (s *Balancer) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.checkOutbounds(ctx, true)
}

func (s *Balancer) checkOutbounds(ctx context.Context, force bool) (map[string]uint16, error) {
	result := make(map[string]uint16)
	if s.checking.Swap(true) {
		return result, nil
	}
	defer s.checking.Store(false)
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](s.concurrency))
	checked := make(map[string]bool)
	var resultAccess sync.Mutex
	for _, detour := range s.ordered {
		tag := detour.Tag()
		realTag := RealTag(detour)
		if checked[realTag] {
			continue
		}
		if !force {
			if history := s.history.LoadURLTestHistory(realTag); history != nil && time.Since(history.Time) < s.interval {
				continue
			}
		}
		checked[realTag] = true
		p, loaded := s.outbound.Outbound(realTag)
		if !loaded {
			continue
		}
		b.Go(realTag, func() (any, error) {
			probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			t, err := urltest.URLTest(probeCtx, s.link, p)
			if err != nil {
				// A caller that gave up (the Clash API hands this endpoint a
				// deadline) must not be read as "every node is dead": dropping
				// the history here is what used to wipe a whole subscription's
				// measurements on one impatient group probe.
				if ctx.Err() == nil {
					s.logger.Debug("outbound ", tag, " unavailable: ", err)
					s.history.DeleteURLTestHistory(realTag)
				}
				return nil, nil
			}
			s.logger.Debug("outbound ", tag, " available: ", t, "ms")
			s.history.StoreURLTestHistory(realTag, &adapter.URLTestHistory{
				Time:  time.Now(),
				Delay: t,
			})
			// A node that answers a probe again has served its cooldown.
			s.clearFailure(realTag)
			resultAccess.Lock()
			result[tag] = t
			resultAccess.Unlock()
			return nil, nil
		})
	}
	b.Wait()
	s.updateLeader()
	return result, nil
}

func (s *Balancer) delay(detour adapter.Outbound) uint16 {
	history := s.history.LoadURLTestHistory(RealTag(detour))
	if history == nil || history.Delay == 0 {
		return timeoutDelay
	}
	return history.Delay
}

func (s *Balancer) cooling(detour adapter.Outbound, now time.Time) bool {
	s.failAccess.Lock()
	defer s.failAccess.Unlock()
	state, found := s.failures[RealTag(detour)]
	return found && now.Before(state.until)
}

func (s *Balancer) clearFailure(tag string) {
	s.failAccess.Lock()
	defer s.failAccess.Unlock()
	delete(s.failures, tag)
}

// penalize records that an outbound failed to carry traffic. The measurement
// goes with it — a node that cannot dial is not "fast", whatever it probed at —
// and a cooldown keeps it out of the election even if a probe revives it a
// second later.
func (s *Balancer) penalize(detour adapter.Outbound) {
	tag := RealTag(detour)
	measured := s.history.LoadURLTestHistory(tag) != nil
	s.failAccess.Lock()
	state := s.failures[tag]
	state.streak++
	backoff := s.cooldown << min(state.streak-1, 4)
	if backoff > maxFailureCooldown {
		backoff = maxFailureCooldown
	}
	state.until = time.Now().Add(backoff)
	s.failures[tag] = state
	s.failAccess.Unlock()
	s.history.DeleteURLTestHistory(tag)
	// Nothing was measured on this one, so the election below cannot tell it
	// apart from every other unmeasured member and would hand it right back.
	// Advancing the rotation is what makes a failure move the group forward.
	if !measured {
		s.rotation.Add(1)
	}
	s.updateLeader()
}

// elect picks the outbound the group should be on: the lowest measured delay
// among members that are not cooling down. Failing that it falls back to the
// best measurement regardless of cooldown, and with no measurements at all it
// walks the members in rotation instead of pinning the first one forever.
func (s *Balancer) elect() adapter.Outbound {
	now := time.Now()
	var (
		best          adapter.Outbound
		bestDelay     = timeoutDelay
		fallback      adapter.Outbound
		fallbackDelay = timeoutDelay
	)
	for _, detour := range s.ordered {
		delay := s.delay(detour)
		if delay == timeoutDelay {
			continue
		}
		if fallback == nil || delay < fallbackDelay {
			fallback, fallbackDelay = detour, delay
		}
		if s.cooling(detour, now) {
			continue
		}
		if best == nil || delay < bestDelay {
			best, bestDelay = detour, delay
		}
	}
	if best != nil {
		return best
	}
	if fallback != nil {
		return fallback
	}
	return s.ordered[int(s.rotation.Load()%uint64(len(s.ordered)))]
}

// updateLeader picks the lowest-delay outbound and, if that is a different one,
// interrupts the connections still running over the previous leader.
func (s *Balancer) updateLeader() {
	best := s.elect()
	if best == nil {
		return
	}
	current := s.leader.Load()
	if current == best {
		return
	}
	// Tolerance keeps the group from flapping between outbounds that measure
	// within noise of each other, since every switch tears down connections.
	// It must not hold a leader that is being punished for failing, though.
	if current != nil && s.tolerance > 0 && !s.cooling(current, time.Now()) {
		if currentDelay := s.delay(current); currentDelay != timeoutDelay && currentDelay < s.delay(best)+s.tolerance {
			return
		}
	}
	s.leader.Store(best)
	s.logger.Info("balancer selected ", best.Tag(), " (", s.delay(best), "ms)")
	s.interruptGroup.Interrupt(s.interruptExternalConnections)
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
		s.penalize(leader)
		return nil, err
	}
	s.clearFailure(RealTag(leader))
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
		s.penalize(leader)
		return nil, err
	}
	s.clearFailure(RealTag(leader))
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
