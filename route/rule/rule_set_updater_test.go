package rule

import (
	"errors"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
)

func TestOptionalRuleSetRetriesOnBackoff(t *testing.T) {
	ruleSet := &RemoteRuleSet{
		options:        option.RuleSet{RemoteOptions: option.RemoteRuleSet{Optional: true}},
		updateInterval: 120 * time.Hour,
	}
	failed := errors.New("unreachable")
	var failures int
	var delays []time.Duration
	for range 8 {
		delays = append(delays, nextUpdateDelay(ruleSet, failed, &failures))
	}
	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		optionalRetryMax, optionalRetryMax, optionalRetryMax,
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("retry %d waits %v, want %v (all: %v)", i, delays[i], want[i], delays)
		}
	}
	if delay := nextUpdateDelay(ruleSet, nil, &failures); delay != ruleSet.updateInterval || failures != 0 {
		t.Errorf("after a success: wait %v with %d failures, want the update interval and none", delay, failures)
	}
}

func TestRuleSetRetryKeepsTheIntervalOnceLoaded(t *testing.T) {
	failed := errors.New("unreachable")
	var failures int
	required := &RemoteRuleSet{updateInterval: 120 * time.Hour}
	if delay := nextUpdateDelay(required, failed, &failures); delay != required.updateInterval {
		t.Errorf("required rule-set waits %v after a failure, want the update interval", delay)
	}
	loaded := &RemoteRuleSet{
		options:        option.RuleSet{RemoteOptions: option.RemoteRuleSet{Optional: true}},
		updateInterval: 120 * time.Hour,
		lastUpdated:    time.Now(),
	}
	if delay := nextUpdateDelay(loaded, failed, &failures); delay != loaded.updateInterval {
		t.Errorf("loaded optional rule-set waits %v after a failed refresh, want the update interval", delay)
	}
}
