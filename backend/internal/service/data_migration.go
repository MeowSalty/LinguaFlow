package service

import (
	"context"
	"encoding/json"
	"entgo.io/ent/dialect"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

const CurrentDataVersion = 3

var ErrDataMigrationRequired = errors.New("database data migration required; start a compatible server version with auto_migrate enabled")

// PreflightDataVersion accepts only supported upgrade starts and runs before
// schema or persistent-format conversion. New/empty databases have no identity.
func PreflightDataVersion(ctx context.Context, client *ent.Client) error {
	query := "SELECT COUNT(*),COALESCE(SUM(CASE WHEN type='table' THEN 1 ELSE 0 END),0) FROM sqlite_schema WHERE name='instance_initializations'"
	if client.DialectName() == dialect.Postgres {
		query = "SELECT COUNT(*),COALESCE(SUM(CASE WHEN c.relkind='r' THEN 1 ELSE 0 END),0) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname='instance_initializations'"
	}
	rows, err := client.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	var count, tables int
	if rows.Next() {
		err = rows.Scan(&count, &tables)
	} else {
		err = ErrInstanceIncomplete
	}
	if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if count != 1 || tables != 1 {
		return ErrInstanceIncomplete
	}
	rows, err = client.QueryContext(ctx, "SELECT id,version,data_version FROM instance_initializations LIMIT 2")
	if err != nil {
		return fmt.Errorf("%w: invalid initialization data version", ErrDataMigrationRequired)
	}
	defer rows.Close()
	count = 0
	for rows.Next() {
		var id, version, dataVersion int
		if err := rows.Scan(&id, &version, &dataVersion); err != nil {
			return ErrDataMigrationRequired
		}
		if id != 1 || version != InitializationVersion {
			return ErrInstanceIncomplete
		}
		if dataVersion < 0 || dataVersion > CurrentDataVersion {
			return ErrDataMigrationRequired
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count > 1 {
		return ErrInstanceIncomplete
	}
	return nil
}

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
		// Version 2 installs the unique checkpoint writer. Existing completed
		// links remain legacy facts; no candidate or successful main response is
		// invented for old jobs. Schema defaults initialize segment versions.
		if marker.DataVersion < 2 {
			after := 0
			for {
				ids, err := tx.Job.Query().Where(job.IDGT(after)).Order(ent.Asc(job.FieldID)).Limit(128).IDs(ctx)
				if err != nil {
					return err
				}
				for _, id := range ids {
					if err := workstate.LockJob(ctx, tx, id); err != nil {
						return err
					}
					if err := workstate.Calibrate(ctx, tx, id); err != nil {
						return err
					}
					after = id
				}
				if len(ids) < 128 {
					break
				}
			}
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
