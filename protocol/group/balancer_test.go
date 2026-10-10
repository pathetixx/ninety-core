package group

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const testDestinationHost = "rule-set.test"

// testMember counts only dials to the test destination: a penalty also starts
// an out-of-band re-probe against the health-check URL, and that one must not
// be mistaken for the connection being retried.
type testMember struct {
	adapter.Outbound
	tag    string
	refuse bool
	dials  atomic.Int32
}

func (m *testMember) Tag() string { return m.tag }

func (m *testMember) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }

func (m *testMember) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if destination.Fqdn == testDestinationHost {
		m.dials.Add(1)
	}
	if m.refuse {
		return nil, E.New("connection refused by ", m.tag)
	}
	client, server := net.Pipe()
	go server.Close()
	return client, nil
}

func (m *testMember) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, E.New("not used")
}

type testOutboundManager struct {
	adapter.OutboundManager
	members map[string]adapter.Outbound
}

func (m *testOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	member, loaded := m.members[tag]
	return member, loaded
}

func newTestBalancer(members ...*testMember) *Balancer {
	manager := &testOutboundManager{members: make(map[string]adapter.Outbound)}
	balancer := &Balancer{
		ctx:            context.Background(),
		outbound:       manager,
		logger:         logger.NOP(),
		history:        urltest.NewHistoryStorage(),
		outbounds:      make(map[string]adapter.Outbound),
		failures:       make(map[string]failureState),
		cooldown:       defaultFailureCooldown,
		interruptGroup: interrupt.NewGroup(),
		close:          make(chan struct{}),
	}
	for _, member := range members {
		manager.members[member.tag] = member
		balancer.tags = append(balancer.tags, member.tag)
		balancer.outbounds[member.tag] = member
		balancer.ordered = append(balancer.ordered, member)
	}
	balancer.leader.Store(balancer.ordered[0])
	return balancer
}

func dialTestDestination(balancer *Balancer) (net.Conn, error) {
	return balancer.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort(testDestinationHost, 443))
}

// Before the first sweep the group picks blind. A placeholder member refusing
// the connection must not fail it: start-up downloads ride on exactly this dial.
func TestBalancerBlindDialMovesToNextMember(t *testing.T) {
	first := &testMember{tag: "placeholder", refuse: true}
	second := &testMember{tag: "dead", refuse: true}
	third := &testMember{tag: "alive"}
	fourth := &testMember{tag: "spare"}
	balancer := newTestBalancer(first, second, third, fourth)

	conn, err := dialTestDestination(balancer)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	conn.Close()
	for _, member := range []*testMember{first, second, third} {
		if got := member.dials.Load(); got != 1 {
			t.Errorf("%s dialed %d times, want 1", member.tag, got)
		}
	}
	if got := fourth.dials.Load(); got != 0 {
		t.Errorf("spare dialed %d times after the connection was up", got)
	}
	if leader := balancer.leader.Load(); leader != third {
		t.Errorf("leader is %s, want alive", leader.Tag())
	}
}

// A measured leader that fails is real news about that member; the connection
// fails and the next one goes to whoever the election picks.
func TestBalancerMeasuredLeaderFailureIsReturned(t *testing.T) {
	first := &testMember{tag: "measured", refuse: true}
	second := &testMember{tag: "other"}
	balancer := newTestBalancer(first, second)
	balancer.history.StoreURLTestHistory(first.tag, &adapter.URLTestHistory{Time: time.Now(), Delay: 50})

	if _, err := dialTestDestination(balancer); err == nil {
		t.Fatal("dial through a failing measured leader succeeded")
	}
	if got := second.dials.Load(); got != 0 {
		t.Errorf("other dialed %d times within the same connection", got)
	}
}

func TestBalancerBlindDialIsBounded(t *testing.T) {
	var members []*testMember
	for _, tag := range []string{"a", "b", "c", "d", "e", "f"} {
		members = append(members, &testMember{tag: tag, refuse: true})
	}
	balancer := newTestBalancer(members...)

	if _, err := dialTestDestination(balancer); err == nil {
		t.Fatal("dial succeeded with every member refusing")
	}
	var total int32
	for _, member := range members {
		total += member.dials.Load()
	}
	if total != maxBlindAttempts {
		t.Errorf("tried %d members, want %d", total, maxBlindAttempts)
	}
}
