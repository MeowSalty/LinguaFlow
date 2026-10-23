package v015

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
	source "github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015/source"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// VerifyPreserved compares every released row and column, except the explicit
// finite-route differences. New tables may add facts but cannot replace IDs,
// ownership edges, task payloads, historical ciphertext or source revisions.
func VerifyPreserved(ctx context.Context, original, target *sql.DB, p *operation.Plan) (map[string]int64, error) {
	counts := map[string]int64{}
	changes, err := preservedChanges(ctx, original, target, p)
	if err != nil {
		return nil, err
	}
	accounts := map[int]operation.Account{}
	for _, a := range p.Accounts {
		accounts[a.UserID] = a
	}
	for _, table := range source.Tables {
		if table.Name == "refresh_tokens" {
			var n int64
			if err := original.QueryRowContext(ctx, "SELECT COUNT(*) FROM refresh_tokens").Scan(&n); err != nil {
				return nil, err
			}
			counts["revoked_refresh_tokens"] = n
			var remaining int
			if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM refresh_tokens").Scan(&remaining); err != nil || remaining != 0 {
				return nil, operation.Fail(operation.NeedsAction, "old refresh sessions remain in target")
			}
			continue
		}
		columns := []string{}
		order := []string{}
		for _, c := range table.Columns {
			columns = append(columns, quoteIdentifier(c.Name))
		}
		for _, c := range table.PrimaryKey {
			order = append(order, quoteIdentifier(c.Name))
		}
		query := "SELECT " + strings.Join(columns, ",") + " FROM " + quoteIdentifier(table.Name) + " ORDER BY " + strings.Join(order, ",")
		before, err := original.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		after, err := target.QueryContext(ctx, query)
		if err != nil {
			before.Close()
			return nil, err
		}
		var n int64
		for before.Next() {
			if !after.Next() {
				before.Close()
				after.Close()
				return nil, operation.Fail(operation.NeedsAction, "target lost a released business row")
			}
			a, b := make([]any, len(columns)), make([]any, len(columns))
			ap, bp := make([]any, len(columns)), make([]any, len(columns))
			for i := range columns {
				ap[i] = &a[i]
				bp[i] = &b[i]
			}
			if err := before.Scan(ap...); err != nil {
				before.Close()
				after.Close()
				return nil, err
			}
			if err := after.Scan(bp...); err != nil {
				before.Close()
				after.Close()
				return nil, err
			}
			id := intValue(a[0])
			values := make(map[string]any, len(columns))
			for i, c := range table.Columns {
				values[c.Name] = a[i]
			}
			for i, c := range table.Columns {
				if changes.allowed(table.Name, c.Name, id, values) {
					continue
				}
				if table.Name == "users" && c.Name == "username" {
					a[i] = accounts[id].Username
				}
				if table.Name == "users" && c.Name == "email" {
					a[i] = nil
					if accounts[id].Email != nil {
						a[i] = *accounts[id].Email
					}
				}
				if table.Name == "system_settings" && c.Name == "value" {
					if value, ok := changes.policies[id]; ok {
						a[i] = value
					}
				}
				if canonicalValue(a[i]) != canonicalValue(b[i]) {
					before.Close()
					after.Close()
					return nil, operation.Fail(operation.NeedsAction, fmt.Sprintf("target changed preserved %s row %d column %s", table.Name, id, c.Name))
				}
			}
			n++
		}
		if err := errors.Join(before.Err(), after.Err()); err != nil {
			before.Close()
			after.Close()
			return nil, err
		}
		var added int64
		for after.Next() {
			added++
		}
		if err := after.Err(); err != nil {
			before.Close()
			after.Close()
			return nil, err
		}
		if added != changes.added[table.Name] {
			before.Close()
			after.Close()
			return nil, operation.Fail(operation.NeedsAction, "target added or lost undeclared "+table.Name+" rows")
		}
		before.Close()
		after.Close()
		counts[table.Name] = n
	}
	return counts, nil
}

type preservedChangeSet struct {
	resources, projects, jobs map[int]bool
	policies                  map[int]string
	legacySpace               int
	added                     map[string]int64
}

func preservedChanges(ctx context.Context, original, target *sql.DB, p *operation.Plan) (*preservedChangeSet, error) {
	c := &preservedChangeSet{resources: map[int]bool{}, projects: map[int]bool{}, jobs: map[int]bool{}, policies: map[int]string{}, added: map[string]int64{}}
	var retention int
	if err := original.QueryRowContext(ctx, "SELECT COUNT(*) FROM system_settings WHERE key='task_retention'").Scan(&retention); err != nil {
		return nil, err
	}
	if retention == 0 {
		var installed int64
		if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM system_settings WHERE key='task_retention'").Scan(&installed); err != nil {
			return nil, err
		}
		c.added["system_settings"] += installed
	}
	if p.StorageManifest == "" {
		return c, nil
	}
	m, err := storagemigrate.ReadManifest(p.StorageManifest)
	if err != nil || operation.Digest(m) != p.StorageDigest {
		return nil, operation.Fail(operation.Incompatible, "immutable storage subplan changed")
	}
	for _, e := range m.Entries {
		if !e.Rejected {
			c.resources[e.ResourceID] = true
		}
	}
	for _, row := range m.Projects {
		c.projects[row.ID] = true
	}
	for _, row := range m.Jobs {
		c.jobs[row.ID] = true
	}
	for _, name := range []string{"blobs", "blob_locations", "source_revisions"} {
		c.added[name] = int64(len(c.resources))
	}
	c.added["storage_tasks"] = 1
	for _, backend := range []string{"legacy", m.DefaultBackendID} {
		var connectionID int
		err := original.QueryRowContext(ctx, "SELECT id FROM storage_connections WHERE backend_id=$1 AND owner_kind='site'", backend).Scan(&connectionID)
		if errors.Is(err, sql.ErrNoRows) {
			c.added["storage_connections"]++
			c.added["storage_spaces"]++
		} else if err != nil {
			return nil, err
		} else {
			var spaces int
			if err := original.QueryRowContext(ctx, "SELECT COUNT(*) FROM storage_spaces WHERE connection_id=$1", connectionID).Scan(&spaces); err != nil {
				return nil, err
			}
			if spaces == 0 {
				c.added["storage_spaces"]++
			}
		}
	}
	var policyID int
	var policyValue string
	if err := original.QueryRowContext(ctx, "SELECT id,value FROM system_settings WHERE key='storage_policy'").Scan(&policyID, &policyValue); errors.Is(err, sql.ErrNoRows) {
		c.added["system_settings"]++
	} else if err != nil {
		return nil, err
	} else {
		value, changed, err := storagemigrate.CanonicalPolicy(policyValue, m.CapacityBytes, m.LogicalLimitBytes)
		if err != nil {
			return nil, err
		}
		if changed {
			c.policies[policyID] = value
		}
	}
	if err := target.QueryRowContext(ctx, "SELECT sp.id FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE co.backend_id='legacy' AND co.owner_kind='site'").Scan(&c.legacySpace); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *preservedChangeSet) allowed(table, column string, id int, before map[string]any) bool {
	switch table {
	case "instance_initializations":
		return column == "data_version" || column == "updated_at"
	case "activity_logs", "usage_records":
		return before["visibility_scope"] == "legacy" && (column == "visibility_scope" || column == "project_activity_logs" || column == "organization_activity_logs" || column == "organization_usage_records")
	case "resources":
		return c.resources[id] && (column == "current_source_revision_id" || column == "source_generation" || column == "updated_at")
	case "projects":
		return c.projects[id] && (column == "storage_space_id" || column == "storage_generation" || column == "updated_at")
	case "jobs":
		return c.jobs[id] && (column == "status" || column == "error_message" || column == "updated_at")
	case "storage_spaces":
		return c.legacySpace == id && (column == "live_bytes" || column == "updated_at")
	case "system_settings":
		_, changed := c.policies[id]
		return changed && column == "updated_at"
	}
	return false
}
func intValue(value any) int {
	switch v := value.(type) {
	case int64:
		return int(v)
	case int:
		return v
	}
	return 0
}
func canonicalValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case []byte:
		return "bytes:" + hex.EncodeToString(v)
	case time.Time:
		return "time:" + v.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%T:%v", v, v)
	}
}

func VerifyAccounts(ctx context.Context, client *ent.Client, p *operation.Plan, keys *credential.Keyring, requireData bool) error {
	users, err := client.User.Query().Order(ent.Asc("id")).All(ctx)
	if err != nil {
		return err
	}
	if len(users) != len(p.Accounts) {
		return operation.Fail(operation.NeedsAction, "target account IDs differ from plan")
	}
	for i, u := range users {
		a := p.Accounts[i]
		purpose := accountidentity.Public
		if u.Role == "admin" {
			purpose = accountidentity.Administrator
		}
		if p.Source.Mode == "local" && u.Username == "local" {
			purpose = accountidentity.Local
		}
		if u.ID != a.UserID || u.Username != a.Username || u.Email != (func() string {
			if a.Email == nil {
				return ""
			}
			return *a.Email
		})() || accountidentity.ValidateAllocation(u.Username, purpose) != nil || !validBcrypt(u.PasswordHash) || u.AuthVersion != 1 || u.VerifiedEmailKey != "" || u.EmailVerifiedAt != nil || u.EmailVerificationSource != "" || u.PasswordVerifiedHash != "" || u.PasswordVerifiedAt != nil {
			return operation.Fail(operation.NeedsAction, "target account evidence differs from plan")
		}
	}
	policy, err := client.LocalAccountPolicy.Get(ctx, 1)
	if err != nil {
		return err
	}
	registration, err := client.SystemSetting.Query().Where(systemsetting.KeyEQ(service.SettingRegistrationEnabled)).Only(ctx)
	if err != nil {
		return err
	}
	if !policy.PasswordLoginEnabled || !policy.UsernameLoginEnabled || policy.EmailLoginEnabled || policy.SignupEmailRequirement != "required" || policy.SignupEnabled != (registration.Value == "true") || policy.SignupRules.Mode != "unrestricted" {
		return operation.Fail(operation.NeedsAction, "target login or registration policy differs from released contract")
	}
	connection, err := client.AuthConnection.Get(ctx, "password")
	if err != nil || connection.Kind != authconnection.KindPassword {
		return operation.Fail(operation.NeedsAction, "built-in password session identity is missing")
	}
	for _, count := range []func(context.Context) (int, error){client.ExternalIdentity.Query().Count, client.LoginBinding.Query().Count, client.RefreshSession.Query().Count, client.RefreshToken.Query().Count} {
		n, err := count(ctx)
		if err != nil {
			return err
		}
		if n != 0 {
			return operation.Fail(operation.NeedsAction, "target contains undeclared authentication or session facts")
		}
	}
	if requireData {
		if err := service.ValidateDataVersion(ctx, client); err != nil {
			return err
		}
	}
	return nil
}

func VerifyShape(ctx context.Context, db *sql.DB, p *operation.Plan) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tables, err := inspectTables(ctx, tx, p.Source.Driver)
	if err != nil {
		return err
	}
	// Current columns are checked here before final auth/version metadata exist.
	return matchTables(tables, migrate.Tables, p.Source.Driver)
}

func CheckConfigurationIdentity(cfg config.ServerConfig, p *operation.Plan) error {
	mode := "serve"
	if cfg.IsLocal() {
		mode = "local"
	}
	if cfg.Database.Driver != p.Source.Driver || mode != p.Source.Mode {
		return operation.Fail(operation.BaselineChanged, "database driver or instance mode changed")
	}
	return nil
}
