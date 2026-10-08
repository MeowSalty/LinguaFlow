package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
)

const CurrentDataVersion = 1

var ErrDataMigrationRequired = errors.New("database data migration required; start a compatible server version with auto_migrate enabled")

// MigrateData upgrades existing initialized instances. Empty instances are left
// for explicit initialization; this entry point never creates an identity.
// Legacy task anchors are assigned only after worker recovery has drained.
func MigrateData(ctx context.Context, client *ent.Client) error {
	return withOrganizationTransaction(ctx, client, func(tx *ent.Client) error {
		if _, err := tx.ExecContext(ctx, "UPDATE instance_initializations SET data_version = data_version WHERE id = 1"); err != nil {
			return err
		}
		rows, err := tx.InstanceInitialization.Query().All(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if len(rows) != 1 || rows[0].ID != 1 || rows[0].Version != InitializationVersion {
			return ErrInstanceIncomplete
		}
		marker := rows[0]
		if marker.DataVersion < 0 || marker.DataVersion > CurrentDataVersion {
			return ErrDataMigrationRequired
		}
		if marker.DataVersion == CurrentDataVersion {
			return nil
		}
		row, err := tx.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingTaskRetention)).Only(ctx)
		if ent.IsNotFound(err) {
			data, marshalErr := json.Marshal(defaultTaskRetention())
			if marshalErr != nil {
				return marshalErr
			}
			if err = tx.SystemSetting.Create().SetKey(SettingTaskRetention).SetValue(string(data)).Exec(ctx); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if _, err = parseTaskRetention(row.Value); err != nil {
			return fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
		}
		return tx.InstanceInitialization.UpdateOneID(marker.ID).SetDataVersion(CurrentDataVersion).Exec(ctx)
	})
}

// ValidateDataVersion is read-only. A current marker never repairs a missing or
// damaged policy: domain readers report that fault without resetting policy.
func ValidateDataVersion(ctx context.Context, client *ent.Client) error {
	rows, err := client.InstanceInitialization.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDataMigrationRequired, err)
	}
	if len(rows) == 0 {
		return nil
	}
	if len(rows) != 1 || rows[0].ID != 1 || rows[0].Version != InitializationVersion {
		return ErrInstanceIncomplete
	}
	if rows[0].DataVersion != CurrentDataVersion {
		return ErrDataMigrationRequired
	}
	return nil
}
