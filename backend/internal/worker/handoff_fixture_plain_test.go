//go:build !race

package worker

import "time"

const (
	handoffFixtureLifetime    = 30 * time.Second
	handoffFixtureBarrierWait = 10 * time.Second
	handoffFixtureJoinWait    = 10 * time.Second
)
