package v015

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/localaccountpolicy"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// ConvertSchema implements the finite v0.15 route on the protected execution DB.
// SQLite needs a full users rebuild: append-only schema creation would retain
// the old email unique constraint. PostgreSQL uses explicit constraint DDL.
func ConvertSchema(ctx context.Context, db *sql.DB, p *operation.Plan) error {
	return operation.Step(ctx, db, p.Source.Driver, p, "schema", func(c *ent.Client) error {
		var allocations []allocationEvidence
		if p.Source.Driver == "sqlite" {
			var err error
			allocations, err = sqliteAllocations(ctx, c)
			if err != nil {
				return err
			}
		}
		if _, err := c.ExecContext(ctx, "DELETE FROM refresh_tokens"); err != nil {
			return err
		}
		if p.Source.Driver == "sqlite" {
			if err := rebuildSQLiteUsers(ctx, c, p); err != nil {
				return err
			}
		} else if p.Source.Driver == "postgres" {
			if err := alterPostgresUsers(ctx, c); err != nil {
				return err
			}
		} else {
			return operation.Fail(operation.UnsupportedTarget, "unsupported database driver")
		}
		if err := c.Schema.Create(ctx); err != nil {
			return err
		}
		if p.Source.Driver == "sqlite" {
			return restoreSQLiteAllocations(ctx, c, allocations)
		}
		return nil
	})
}

const sqliteUsersDDL = `CREATE TABLE users (
id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
created_at datetime NOT NULL, updated_at datetime NOT NULL,
username text NOT NULL UNIQUE,
password_hash text NULL, password_verified_hash text NULL, password_verified_at datetime NULL,
email text NULL, email_verified_at datetime NULL, email_verification_source text NULL,
verified_email_key text NULL UNIQUE, auth_version bigint NOT NULL DEFAULT 1,
display_name text NULL, role text NOT NULL DEFAULT 'user', active bool NOT NULL DEFAULT true,
CONSTRAINT user_auth_version_safe CHECK (auth_version >= 1 AND auth_version <= 9007199254740991),
CONSTRAINT user_email_verification_consistent CHECK ((verified_email_key IS NULL AND email_verified_at IS NULL AND email_verification_source IS NULL) OR (email IS NOT NULL AND verified_email_key IS NOT NULL AND verified_email_key = email AND email_verified_at IS NOT NULL AND email_verification_source IS NOT NULL AND email_verification_source <> ''))
)`

func rebuildSQLiteUsers(ctx context.Context, c *ent.Client, p *operation.Plan) error {
	var sequence int64
	rows, err := c.QueryContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name='users'")
	if err != nil {
		return err
	}
	if rows.Next() {
		err = rows.Scan(&sequence)
	}
	if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, "CREATE TABLE migration_v015_users AS SELECT * FROM users"); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, "DROP TABLE users"); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, sqliteUsersDDL); err != nil {
		return err
	}
	var total int
	rows, err = c.QueryContext(ctx, "SELECT COUNT(*) FROM migration_v015_users")
	if err != nil {
		return err
	}
	if rows.Next() {
		err = rows.Scan(&total)
	}
	if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	if total != len(p.Accounts) {
		return operation.Fail(operation.BaselineChanged, "account set changed")
	}
	for _, a := range p.Accounts {
		result, err := c.ExecContext(ctx, `INSERT INTO users (id,created_at,updated_at,username,password_hash,email,display_name,role,active)
SELECT id,created_at,updated_at,?,password_hash,?,display_name,role,active FROM migration_v015_users WHERE id=?`, a.Username, a.Email, a.UserID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return operation.Fail(operation.BaselineChanged, "mapped user is missing")
		}
	}
	if _, err := c.ExecContext(ctx, "UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name='users'", sequence); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "DROP TABLE migration_v015_users")
	return err
}

func alterPostgresUsers(ctx context.Context, c *ent.Client) error {
	// The released Ent schema creates column uniqueness as users_email_key.
	// Refuse unknown constraints rather than executing operator-provided DDL.
	rows, err := c.QueryContext(ctx, `SELECT i.relname,COALESCE(con.conname,''),array_length(ix.indkey,1)
FROM pg_index ix JOIN pg_class t ON t.oid=ix.indrelid JOIN pg_namespace ns ON ns.oid=t.relnamespace
JOIN pg_class i ON i.oid=ix.indexrelid JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=ANY(ix.indkey)
LEFT JOIN pg_constraint con ON con.conindid=ix.indexrelid
WHERE ns.nspname=current_schema() AND t.relname='users' AND a.attname='email' AND ix.indisunique`)
	if err != nil {
		return err
	}
	var statements []string
	for rows.Next() {
		var index, constraint string
		var columns int
		if err := rows.Scan(&index, &constraint, &columns); err != nil {
			rows.Close()
			return err
		}
		if columns != 1 || index != "users_email_key" || (constraint != "" && constraint != "users_email_key") {
			rows.Close()
			return operation.Fail(operation.UnsupportedSource, "unknown legacy email uniqueness constraint")
		}
		if constraint != "" {
			statements = append(statements, "ALTER TABLE users DROP CONSTRAINT users_email_key")
		} else {
			statements = append(statements, "DROP INDEX users_email_key")
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(statements) != 1 {
		return operation.Fail(operation.UnsupportedSource, "legacy email uniqueness is missing or ambiguous")
	}
	statements = append(statements, "ALTER TABLE users ALTER COLUMN email DROP NOT NULL", "ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL")
	for _, statement := range statements {
		if _, err := c.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// ConvertAccounts never changes identity IDs, password bytes, roles or timestamps.
// PostgreSQL first vacates every name so validated exchanges are possible.
func ConvertAccounts(ctx context.Context, db *sql.DB, p *operation.Plan) error {
	return operation.Step(ctx, db, p.Source.Driver, p, "accounts", func(c *ent.Client) error {
		if p.Source.Driver == "postgres" {
			for _, a := range p.Accounts {
				if _, err := c.ExecContext(ctx, "UPDATE users SET username=$1 WHERE id=$2", "migration-"+p.ID+"-"+strconv.Itoa(a.UserID), a.UserID); err != nil {
					return err
				}
			}
			for _, a := range p.Accounts {
				result, err := c.ExecContext(ctx, "UPDATE users SET username=$1,email=$2,auth_version=1 WHERE id=$3", a.Username, a.Email, a.UserID)
				if err != nil {
					return err
				}
				n, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if n != 1 {
					return operation.Fail(operation.BaselineChanged, "mapped account is missing")
				}
			}
		}
		setting, err := c.SystemSetting.Query().Where(systemsetting.KeyEQ(service.SettingRegistrationEnabled)).Only(ctx)
		if err != nil {
			return operation.Fail(operation.NeedsAction, "legacy registration policy is unavailable")
		}
		registration, err := strconv.ParseBool(setting.Value)
		if err != nil {
			return operation.Fail(operation.NeedsAction, "legacy registration policy is invalid")
		}
		if _, err := c.AuthConnection.Create().SetID("password").SetKind(authconnection.KindPassword).SetCurrentRevision(1).Save(ctx); err != nil {
			return err
		}
		_, err = c.LocalAccountPolicy.Create().SetID(1).SetSignupEnabled(registration).SetSignupEmailRequirement(localaccountpolicy.SignupEmailRequirementRequired).
			SetPasswordLoginEnabled(true).SetUsernameLoginEnabled(true).SetEmailLoginEnabled(false).SetSignupRules(authrules.Unrestricted()).Save(ctx)
		if err != nil {
			return err
		}
		if p.Source.Driver == "postgres" {
			// This new singleton is inserted with an explicit ID. PostgreSQL does
			// not advance its owned sequence for that insert; align only the new
			// target sequence, idempotently even after a rolled-back attempt.
			_, err = c.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence('local_account_policies','id'),1,true)")
		}
		return err
	})
}

func ConvertHistory(ctx context.Context, db *sql.DB, p *operation.Plan) error {
	return operation.Step(ctx, db, p.Source.Driver, p, "history", func(c *ent.Client) error {
		return service.MigrateHistoryVisibility(ctx, c)
	})
}

// Complete performs the business version update and publishes all readiness
// metadata in the same transaction, after the caller has verified the target.
func Complete(ctx context.Context, db *sql.DB, p *operation.Plan, verify func(*ent.Client) error) error {
	return operation.StepWithResult(ctx, db, p.Source.Driver, p, "ready", func(c *ent.Client) (string, error) {
		if err := service.MigrateData(ctx, c); err != nil {
			return "", err
		}
		if err := verify(c); err != nil {
			return "", err
		}
		if err := NormalizeBaselineTimestamps(ctx, c, p); err != nil {
			return "", err
		}
		if _, err := c.ExecContext(ctx, "CREATE TABLE linguaflow_auth_format (id INTEGER NOT NULL PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL CHECK(version>0))"); err != nil {
			return "", err
		}
		if _, err := c.ExecContext(ctx, "INSERT INTO linguaflow_auth_format(id,version) VALUES(1,1)"); err != nil {
			return "", err
		}
		if err := VerifyBaselineClient(ctx, c, p, true); err != nil {
			return "", err
		}
		if err := service.ValidateDataVersion(ctx, c); err != nil {
			return "", err
		}
		result, err := BusinessFingerprintClient(ctx, c, p.Source.Driver)
		if err != nil {
			return "", err
		}
		return result, operation.MarkReady(ctx, c, p)
	})
}

func quoteIdentifier(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
func placeholders(driver string, count int) string {
	result := make([]string, count)
	for i := range result {
		result[i] = "?"
		if driver == "postgres" {
			result[i] = fmt.Sprintf("$%d", i+1)
		}
	}
	return strings.Join(result, ",")
}
