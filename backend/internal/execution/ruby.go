package execution

import "errors"

const (
	SnapshotSchemaVersion       = 2
	SnapshotDefaultsVersion     = 2
	DefaultRubyRetryConcurrency = 1
	RubyProtocolVersion         = 2
	RubyValidatorVersion        = 1
)

type ConcurrencyModel string

const (
	LegacyRoundShared ConcurrencyModel = "legacy_round_shared"
	StageSeparated    ConcurrencyModel = "stage_separated"
)

// ConcurrencyModelForSpec identifies frozen execution semantics, independently
// of the Ruby wire protocol. It never fills missing values on resumed snapshots.
func ConcurrencyModelForSpec(spec *JobExecutionSnapshot) (ConcurrencyModel, error) {
	if spec == nil {
		return "", errors.New("missing execution snapshot")
	}
	if spec.SchemaVersion == 1 && spec.DefaultsVersion == 1 {
		return LegacyRoundShared, nil
	}
	if spec.SchemaVersion == SnapshotSchemaVersion && spec.DefaultsVersion == SnapshotDefaultsVersion {
		return StageSeparated, nil
	}
	return "", errors.New("unsupported or missing execution snapshot version")
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

// EffectiveRubyProtocolVersion adapts old frozen snapshots without modifying
// them. New snapshots must carry the explicit protocol and validator versions.
func EffectiveRubyProtocolVersion(spec *JobExecutionSnapshot) int {
	if spec != nil && spec.SchemaVersion == 1 && spec.DefaultsVersion == 1 {
		return 1
	}
	if spec == nil {
		return 0
	}
	return spec.RubyProtocolVersion
}
