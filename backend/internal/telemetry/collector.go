// Package telemetry records only process-local, fixed-cardinality runtime metrics.
package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

var providers = [...]string{"openai", "anthropic", "google", "other"}
var operations = [...]string{"generate", "list_models"}
var outcomes = [...]string{"success", "http_error", "transport_error", "timeout", "cancelled"}

type OutcomeSnapshot struct {
	Outcome       string  `json:"outcome"`
	Finished      int64   `json:"http_attempts_finished_total"`
	DurationSum   float64 `json:"http_attempt_duration_seconds_sum"`
	DurationCount int64   `json:"http_attempt_duration_seconds_count"`
}

type ExternalRequestSnapshot struct {
	Provider  string            `json:"provider"`
	Operation string            `json:"operation"`
	Inflight  int64             `json:"http_attempts_inflight"`
	Total     int64             `json:"http_attempts_total"`
	Outcomes  []OutcomeSnapshot `json:"outcomes"`
}

type Snapshot struct {
	InstanceID       string                    `json:"instance_id"`
	StartedAt        time.Time                 `json:"started_at"`
	AsOf             time.Time                 `json:"as_of"`
	UptimeSeconds    float64                   `json:"uptime_seconds"`
	Scope            string                    `json:"scope"`
	ExternalRequests []ExternalRequestSnapshot `json:"external_requests"`
}

// Collector owns one server's counters. The lock makes total = finished + inflight
// hold even when a snapshot races request completion.
type Collector struct {
	mu         sync.Mutex
	instanceID string
	started    time.Time
	requests   [4][2]ExternalRequestSnapshot
}

func NewCollector() *Collector {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	c := &Collector{instanceID: hex.EncodeToString(id), started: time.Now()}
	for p, provider := range providers {
		for o, operation := range operations {
			x := &c.requests[p][o]
			x.Provider, x.Operation = provider, operation
			x.Outcomes = make([]OutcomeSnapshot, len(outcomes))
			for i, outcome := range outcomes {
				x.Outcomes[i].Outcome = outcome
			}
		}
	}
	return c
}

func dimension(value string, values []string, fallback int) int {
	for i, v := range values {
		if v == value {
			return i
		}
	}
	return fallback
}

func (c *Collector) begin(provider, operation string) func(string, time.Duration) {
	p, o := dimension(provider, providers[:], 3), dimension(operation, operations[:], 0)
	c.mu.Lock()
	c.requests[p][o].Total++
	c.requests[p][o].Inflight++
	c.mu.Unlock()
	return func(outcome string, duration time.Duration) {
		c.mu.Lock()
		defer c.mu.Unlock()
		x := &c.requests[p][o]
		x.Inflight--
		r := &x.Outcomes[dimension(outcome, outcomes[:], 2)]
		r.Finished++
		r.DurationCount++
		r.DurationSum += duration.Seconds()
	}
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	s := Snapshot{InstanceID: c.instanceID, StartedAt: c.started.UTC(), AsOf: now.UTC(), UptimeSeconds: now.Sub(c.started).Seconds(), Scope: "instance", ExternalRequests: make([]ExternalRequestSnapshot, 0, 8)}
	for _, row := range c.requests {
		for _, item := range row {
			item.Outcomes = append([]OutcomeSnapshot(nil), item.Outcomes...)
			s.ExternalRequests = append(s.ExternalRequests, item)
		}
	}
	return s
}
