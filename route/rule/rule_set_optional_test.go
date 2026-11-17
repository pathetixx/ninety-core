package rule

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

func testRemoteRuleSet(server *httptest.Server, optional bool) *RemoteRuleSet {
	return &RemoteRuleSet{
		ctx:        context.Background(),
		logger:     log.NewNOPFactory().NewLogger("rule-set"),
		tag:        "test",
		url:        server.URL,
		options:    option.RuleSet{RemoteOptions: option.RemoteRuleSet{Optional: optional}},
		httpClient: server.Client(),
	}
}

// A server that accepts the request and never answers: the outbound that
// connects and then stalls, which a refusal-only fallback never moves past.
func stallingServer(t *testing.T) *httptest.Server {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	return server
}

func TestOptionalRuleSetGivesUpAtTheStartBudget(t *testing.T) {
	ruleSet := testRemoteRuleSet(stallingServer(t), true)
	ctx := ContextWithOptionalStartDeadline(context.Background(), time.Now().Add(100*time.Millisecond))
	started := time.Now()
	err := ruleSet.initialFetch(ctx)
	if !errors.Is(err, errOptionalStartBudget) {
		t.Fatalf("initial download of a stalled optional set returned %v, want the start budget error", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("initial download gave up after %v, want about the 100ms budget", elapsed)
	}
}

func TestOptionalRuleSetSharesAnExpiredBudget(t *testing.T) {
	ruleSet := testRemoteRuleSet(stallingServer(t), true)
	// A set of the second concurrency wave starts after the budget is spent.
	ctx := ContextWithOptionalStartDeadline(context.Background(), time.Now().Add(-time.Second))
	if err := ruleSet.initialFetch(ctx); !errors.Is(err, errOptionalStartBudget) {
		t.Fatalf("initial download after the budget returned %v, want the start budget error", err)
	}
}

func TestRequiredRuleSetIgnoresTheStartBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		http.NotFound(w, r)
	}))
	defer server.Close()
	ruleSet := testRemoteRuleSet(server, false)
	ctx := ContextWithOptionalStartDeadline(context.Background(), time.Now().Add(50*time.Millisecond))
	err := ruleSet.initialFetch(ctx)
	if err == nil || errors.Is(err, errOptionalStartBudget) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("required set returned %v, want the server's 404 after waiting past the budget", err)
	}
}

func TestOptionalRuleSetWithoutABudgetWaitsForTheServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		http.NotFound(w, r)
	}))
	defer server.Close()
	ruleSet := testRemoteRuleSet(server, true)
	err := ruleSet.initialFetch(context.Background())
	if err == nil || errors.Is(err, errOptionalStartBudget) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("optional set started without a budget returned %v, want the server's 404", err)
	}
}

func TestOptionalRuleSetLeftEmptyIsRetriedSoon(t *testing.T) {
	now := time.Now()
	optional := option.RuleSet{RemoteOptions: option.RemoteRuleSet{Optional: true}}
	empty := &RemoteRuleSet{options: optional, updateInterval: 120 * time.Hour}
	if got := firstUpdate(empty, now); !got.Equal(now.Add(optionalFirstRetry)) {
		t.Errorf("empty optional set first updates at %v, want %v", got.Sub(now), optionalFirstRetry)
	}
	loaded := &RemoteRuleSet{options: optional, updateInterval: 120 * time.Hour, lastUpdated: now.Add(-time.Hour)}
	if got, want := firstUpdate(loaded, now), loaded.lastUpdated.Add(loaded.updateInterval); !got.Equal(want) {
		t.Errorf("loaded optional set first updates at %v, want its update interval at %v", got, want)
	}
	required := &RemoteRuleSet{updateInterval: 120 * time.Hour}
	if got := firstUpdate(required, now); got.After(now) {
		t.Errorf("never-loaded required set first updates %v from now, want right away as upstream", got.Sub(now))
	}
}
