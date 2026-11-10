package execution

import (
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	SnapshotSchemaVersion       = 4
	SnapshotDefaultsVersion     = 4
	DefaultRubyRetryConcurrency = 1
	DefaultRubyBatchSize        = 1
	DefaultRubyBatchWaitMS      = 25
	RubyProtocolVersion         = 2
	RubyValidatorVersion        = 1
	RubyBatchProtocolVersion    = 1
)

type ConcurrencyModel string

const (
	StageSeparated ConcurrencyModel = "stage_separated"
)

// ConcurrencyModelForSpec identifies frozen execution semantics, independently
// of the Ruby wire protocol. It never fills missing values on resumed snapshots.
func ConcurrencyModelForSpec(spec *JobExecutionSnapshot) (ConcurrencyModel, error) {
	if spec == nil {
		return "", errors.New("missing execution snapshot")
	}
	if err := ValidateExecutionFormat(spec.SchemaVersion, spec.DefaultsVersion, spec.ResponseFormatVersion); err != nil {
		return "", err
	}
	return StageSeparated, nil
}

// RubyRetryBatchConfig contains resolved content limits. They are upper bounds;
// one oversized segment is still dispatched on its own by the scheduler.
type RubyRetryBatchConfig struct {
	BatchSize        int
	MaxWordsPerBatch int
	BatchWaitMS      int
}

func ResolveRubyRetryBatch(batchSize, maxWordsPerBatch, waitMS *int) (RubyRetryBatchConfig, error) {
	config := RubyRetryBatchConfig{BatchSize: DefaultRubyBatchSize, BatchWaitMS: DefaultRubyBatchWaitMS}
	if batchSize != nil {
		config.BatchSize = *batchSize
	}
	if maxWordsPerBatch != nil {
		config.MaxWordsPerBatch = *maxWordsPerBatch
	}
	if waitMS != nil {
		config.BatchWaitMS = *waitMS
	}
	if err := ValidateBatchLimits("ruby_retry", config.BatchSize, config.MaxWordsPerBatch); err != nil {
		return RubyRetryBatchConfig{}, fmt.Errorf("ruby_retry.%w", err)
	}
	if config.BatchWaitMS < 0 || int64(config.BatchWaitMS) > math.MaxInt64/int64(time.Millisecond) {
		return RubyRetryBatchConfig{}, errors.New("ruby_retry.batch_wait_ms must be a nonnegative duration in range")
	}
	return config, nil
}

// EffectiveRubyRetryBatch reads validated current frozen values.
func EffectiveRubyRetryBatch(spec *JobExecutionSnapshot) RubyRetryBatchConfig {
	if spec == nil || spec.RubyRetry == nil || !spec.RubyRetry.Enabled {
		return RubyRetryBatchConfig{BatchSize: 1}
	}
	rr := spec.RubyRetry
	return RubyRetryBatchConfig{BatchSize: ptrInt(rr.BatchSize), MaxWordsPerBatch: ptrInt(rr.MaxWordsPerBatch), BatchWaitMS: ptrInt(rr.BatchWaitMS)}
}

func EffectiveRubyBatchProtocolVersion(spec *JobExecutionSnapshot) int {
	if spec == nil {
		return 0
	}
	return spec.RubyBatchProtocolVersion
}

func ResolveRubyRetryConcurrency(input *int) (int, error) {
	if input == nil {
		return DefaultRubyRetryConcurrency, nil
	}
	if *input < 1 {
		return 0, errors.New("ruby_retry.concurrency must be positive")
	}
	return *input, nil
}

// EffectiveRubyProtocolVersion reads the explicit current protocol version.
func EffectiveRubyProtocolVersion(spec *JobExecutionSnapshot) int {
	if spec == nil {
		return 0
	}
	return spec.RubyProtocolVersion
}
