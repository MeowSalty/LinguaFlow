//go:build !race

package worker

import "time"

const (
	// Functional completion watchdog, separate from timeout/cancellation tests.
	handoffFixtureFunctionalWait = 30 * time.Second
	handoffFixtureLifetime       = 30 * time.Second
	handoffFixtureBarrierWait    = 10 * time.Second
	handoffFixtureJoinWait       = 10 * time.Second
)
