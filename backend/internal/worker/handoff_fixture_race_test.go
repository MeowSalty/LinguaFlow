//go:build race

package worker

import "time"

// These budgets belong only to the three real HTTP barrier fixtures. Production
// queue waits, dispatch leases and paid-response deadlines are unchanged.
const (
	// Race instrumentation and shared runners need the existing two-minute
	// fixture wait allowance; production handoff deadlines remain native.
	handoffFixtureFunctionalWait = 2 * time.Minute
	handoffFixtureLifetime       = 5 * time.Minute
	handoffFixtureBarrierWait    = 2 * time.Minute
	handoffFixtureJoinWait       = 30 * time.Second
)
