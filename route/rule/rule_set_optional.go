package rule

import (
	"context"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// An optional rule-set must not hold the start hostage. Its first download goes
// out through the very outbounds the box is still bringing up - for Ninety, a
// balancer that picks blind until its first health check - and a member that
// stalls instead of refusing kept the start waiting past the point where the app
// gives up on the core. The initial downloads of all optional sets share one
// budget; whatever has not arrived by then starts empty and comes in through the
// updater.
const (
	OptionalRuleSetStartBudget = 3 * time.Second
	// The first background attempt waits for the outbound groups' first health
	// check, so it does not dial blind a second time.
	optionalFirstRetry = 15 * time.Second
	// The updater fetches the sets one after another: one stalled download would
	// otherwise hold back every other set's retry.
	optionalFetchTimeout = time.Minute
)

var errOptionalStartBudget = E.New("not downloaded within the start budget")

type optionalStartDeadlineKey struct{}

// ContextWithOptionalStartDeadline sets when the initial download of every
// optional remote rule-set started under ctx is abandoned.
func ContextWithOptionalStartDeadline(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, optionalStartDeadlineKey{}, deadline)
}

func optionalStartDeadline(ctx context.Context) (time.Time, bool) {
	deadline, loaded := ctx.Value(optionalStartDeadlineKey{}).(time.Time)
	return deadline, loaded
}

// initialFetch is the download at start. Only an optional set is bounded by the
// start budget; a required one fails the start as upstream does, so it keeps
// waiting as long as upstream would.
func (s *RemoteRuleSet) initialFetch(ctx context.Context) error {
	deadline, loaded := optionalStartDeadline(ctx)
	if !s.options.RemoteOptions.Optional || !loaded {
		return s.fetch(ctx, true)
	}
	fetchCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	err := s.fetch(fetchCtx, true)
	if err != nil && fetchCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return errOptionalStartBudget
	}
	return err
}

// firstUpdate is when the updater first visits a set. A loaded set waits out its
// interval as upstream does; an optional set the start left empty is retried
// soon, once the outbound groups have measured their members.
func firstUpdate(ruleSet *RemoteRuleSet, now time.Time) time.Time {
	if ruleSet.options.RemoteOptions.Optional && ruleSet.lastUpdated.IsZero() {
		return now.Add(optionalFirstRetry)
	}
	return ruleSet.lastUpdated.Add(ruleSet.updateInterval)
}
