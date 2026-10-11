package pipeline

import (
	"math"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// AlignmentBatchConfig is frozen by the execution resolver. Its zero value
// preserves single-paragraph behavior for legacy snapshots and direct callers.
type AlignmentBatchConfig struct {
	BatchSize        int
	MaxWordsPerBatch int
	Wait             time.Duration
	ProtocolVersion  int
}

func alignmentBatchSettings(handler RoundHandler) AlignmentBatchConfig {
	switch h := handler.(type) {
	case *TranslateHandler:
		return h.RubyBatch
	case *ReviseHandler:
		return h.RubyBatch
	default:
		return AlignmentBatchConfig{}
	}
}

func (c AlignmentBatchConfig) enabled() bool {
	return c.ProtocolVersion == ruby.BatchProtocolVersion && c.BatchSize != 1 && (c.BatchSize > 0 || c.MaxWordsPerBatch > 0)
}

func needsAlignmentAttempt(round Round, c *Candidate) bool {
	backends, budget, _, _, _ := alignmentSettings(round.Handler)
	return !c.Ready && c.Alignment != nil && len(c.Alignment.Missing()) > 0 && len(backends) > 0 && c.LogicalAttempt < budget && c.NetworkAttempt < max(1, round.Retry.MaxAttempts)
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func alignmentWords(c *Candidate) int {
	if c.Alignment == nil {
		return 0
	}
	words := 0
	for _, value := range c.Alignment.AlignmentRequestTexts(rawSource(&c.Segment), c.Alignment.Missing()) {
		n := CountWords(value)
		if n > math.MaxInt-words {
			return math.MaxInt
		}
		words += n
	}
	return words
}

// readyAlignmentBatch selects from a single resource/round's immutable handler
// configuration. Pool, format, epoch and protocol must also match; per-member
// attempt counts deliberately do not have to match.
func readyAlignmentBatch(ready []*Candidate, config AlignmentBatchConfig) (batch []*Candidate, full bool) {
	if len(ready) == 0 {
		return nil, false
	}
	first := ready[0]
	if !config.enabled() || first.ForceSingleAlignment || first.Ready || first.Alignment == nil {
		return ready[:1], true
	}
	words := 0
	constraint := BatchConstraint{MaxSegments: config.BatchSize, MaxWords: config.MaxWordsPerBatch}
	for _, c := range ready {
		if c.ForceSingleAlignment || c.Ready || c.Alignment == nil || c.PoolIndex != first.PoolIndex || c.RetryEpoch != first.RetryEpoch || c.Mode != first.Mode || c.Format != first.Format || c.Alignment.ProtocolVersion != first.Alignment.ProtocolVersion || c.Alignment.ValidatorVersion != first.Alignment.ValidatorVersion {
			continue
		}
		n := alignmentWords(c)
		if len(batch) > 0 && constraint.exceeds(len(batch), words, n) {
			return batch, true
		}
		batch = append(batch, c)
		if n > math.MaxInt-words {
			words = math.MaxInt
		} else {
			words += n
		}
		if (config.BatchSize > 0 && len(batch) >= config.BatchSize) || (config.MaxWordsPerBatch > 0 && words >= config.MaxWordsPerBatch) {
			return batch, true
		}
	}
	return batch, false
}
