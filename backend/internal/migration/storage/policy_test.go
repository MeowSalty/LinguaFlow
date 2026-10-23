package storagemigrate

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
)

func TestStoragePolicyBaselineAllowsOnlyExplicitQuotaCompletion(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	const original = `{"mode":"site_only","default_choice":"site","generation":9,"existing_rule":{"enabled":false},"existing_numbers":9007199254740993}`
	row, err := f.client.SystemSetting.Create().SetKey("storage_policy").SetValue(original).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, maxIDs, err := Baseline(ctx, f.migration.db, manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.migration.Apply(ctx, manifest, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.migration.Verify(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	after, _, err := Baseline(ctx, f.migration.db, manifest, maxIDs)
	if err != nil || before != after {
		t.Fatal("declared quota completion changed baseline", err)
	}
	value, _, err := CanonicalPolicy(original, manifest.CapacityBytes, manifest.LogicalLimitBytes)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := f.client.SystemSetting.Query().Where(systemsetting.IDEQ(row.ID)).Only(ctx)
	if err != nil || actual.Value != value {
		t.Fatal("released policy fields changed", err)
	}
	if err := f.client.SystemSetting.UpdateOneID(row.ID).SetValue(`{"mode":"site_only","default_choice":"site","generation":10}`).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	changed, _, err := Baseline(ctx, f.migration.db, manifest, maxIDs)
	if err != nil || changed == before {
		t.Fatal("undeclared policy mutation escaped baseline", err)
	}
	if err := f.client.Job.UpdateOneID(f.job.ID).SetStatus("completed").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.migration.Verify(ctx, manifest); err == nil {
		t.Fatal("undeclared job result escaped verification")
	}
}
