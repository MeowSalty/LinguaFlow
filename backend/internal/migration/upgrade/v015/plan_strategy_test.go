package v015

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func TestNewPlanDefaultsToInplaceAndRetainsCloneWhenTargetProvided(t *testing.T) {
	ctx := context.Background()
	inplaceOptions, _ := fixtureDeployment(t, "serve")
	inplaceOptions.TargetDataDir = ""
	inplace, err := NewPlan(ctx, inplaceOptions)
	if err != nil {
		t.Fatalf("default inplace plan: %v", err)
	}
	if inplace.Version != operation.Version || inplace.Strategy != operation.Inplace || inplace.DatabaseBackup != operation.BackupAuto {
		t.Fatalf("default strategy/protection = %d/%q/%q", inplace.Version, inplace.Strategy, inplace.DatabaseBackup)
	}
	if inplace.Target.DataDir != "" || inplace.Target.ConfigPath != "" || inplace.Target.Identity != "" || inplace.Target.ParentIdentity != "" {
		t.Fatalf("inplace plan contains an independent target: %+v", inplace.Target)
	}
	if len(inplace.LocalRoots) == 0 {
		t.Fatal("inplace plan lost source local roots")
	}
	for _, root := range inplace.LocalRoots {
		if root.Target != root.Source {
			t.Fatalf("inplace plan would copy local objects: %+v", root)
		}
	}
	if !operation.ValidHex(inplace.ObjectsDigest, 64) {
		t.Fatal("inplace plan lost the source object baseline")
	}

	cloneOptions, _ := fixtureDeployment(t, "serve")
	clone, err := NewPlan(ctx, cloneOptions)
	if err != nil {
		t.Fatalf("target-argument clone plan: %v", err)
	}
	if clone.Strategy != operation.Clone || clone.Target.DataDir != filepath.Clean(cloneOptions.TargetDataDir) {
		t.Fatalf("target arguments did not retain clone behavior: %+v", clone)
	}
}

func TestNewPlanRejectsExplicitInplaceWithTargetBeforeSourceAccess(t *testing.T) {
	options, _ := fixtureDeployment(t, "serve")
	options.Strategy = string(operation.Inplace)
	_, err := NewPlan(context.Background(), options)
	var classified *operation.Error
	if !errors.As(err, &classified) || classified.Code != operation.UnsupportedTarget {
		t.Fatalf("explicit inplace plus target error = %v, want unsupported target", err)
	}
}

func TestResolveDatabaseBackupDefaultsByDriverAndStrategy(t *testing.T) {
	tests := []struct {
		name     string
		driver   string
		strategy operation.Strategy
		want     operation.DatabaseBackupMode
	}{
		{name: "sqlite inplace", driver: "sqlite", strategy: operation.Inplace, want: operation.BackupAuto},
		{name: "sqlite clone", driver: "sqlite", strategy: operation.Clone, want: operation.BackupAuto},
		{name: "postgres inplace", driver: "postgres", strategy: operation.Inplace, want: operation.BackupExternal},
		{name: "postgres clone", driver: "postgres", strategy: operation.Clone, want: operation.BackupAuto},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := operation.ResolveBackup("", test.driver, test.strategy)
			if err != nil || got != test.want {
				t.Fatalf("ResolveBackup = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestRehearsalBaselineRequiresCompleteSourceFingerprintAndSchema(t *testing.T) {
	source := &Inspection{Format: "v015", Fingerprint: "source-fingerprint", SchemaDigest: "source-schema"}
	restored, err := validateRehearsalBaseline(source, &Inspection{Format: "v015", Fingerprint: source.Fingerprint, SchemaDigest: source.SchemaDigest})
	if err != nil || !restored {
		t.Fatalf("matching restored source rejected: %v", err)
	}
	for _, rehearsal := range []*Inspection{
		{Format: "v015", Fingerprint: "different-fingerprint", SchemaDigest: source.SchemaDigest},
		{Format: "v015", Fingerprint: source.Fingerprint, SchemaDigest: "different-schema"},
		{Format: "unknown", Fingerprint: source.Fingerprint, SchemaDigest: source.SchemaDigest},
	} {
		if restored, err := validateRehearsalBaseline(source, rehearsal); err == nil || restored {
			t.Fatalf("mismatched restored rehearsal accepted: %+v", rehearsal)
		}
	}
}
