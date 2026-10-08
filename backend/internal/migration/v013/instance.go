package v013

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credentialstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/instanceinitialization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// Report contains migration statistics only; no secrets or connection strings.
type Report struct {
	BackendsMigrated      int  `json:"backends_migrated"`
	BackendsWithoutSecret int  `json:"backends_without_secret"`
	CredentialsCreated    int  `json:"credentials_created"`
	SettingsRemoved       int  `json:"settings_removed"`
	RegistrationEnabled   bool `json:"registration_enabled"`
	Profiles              int  `json:"profiles"`
	Jobs                  int  `json:"jobs"`
}

type legacyBackendMigration struct {
	row     *ent.Backend
	input   credentialstore.CreateInput
	options map[string]any
	hasKey  bool
}

// migrateInstance upgrades the identities, settings and backend credentials
// of a v0.13.0 database after its schema and timestamp format are upgraded.
// The caller must stop all writers and provide a transaction client, rolling the
// transaction back on any error. This function never creates or commits one.
// Historical job snapshots require a separate migration in that same transaction.
func migrateInstance(ctx context.Context, client *ent.Client, keys *credential.Keyring, mode string) (Report, error) {
	report := Report{}
	wantMode := instanceinitialization.ModeServe
	switch mode {
	case "server", "serve":
	case "local":
		wantMode = instanceinitialization.ModeLocal
	default:
		return report, errors.New("legacy migration supports only serve or local mode")
	}
	if client == nil || keys == nil || !keys.HasKey(keys.ActiveKeyID()) {
		return report, errors.New("legacy migration requires a database client and valid credential keyring")
	}
	// Reject a current or partially upgraded database before modifying anything.
	checks := []struct {
		name   string
		exists func(context.Context) (bool, error)
	}{
		{"instance initialization marker", client.InstanceInitialization.Query().Exist},
		{"credentials", client.Credential.Query().Exist},
		{"credential versions", client.CredentialVersion.Query().Exist},
		{"credential job references", client.CredentialJobReference.Query().Exist},
	}
	for _, check := range checks {
		exists, err := check.exists(ctx)
		if err != nil {
			return report, fmt.Errorf("inspect legacy %s: %w", check.name, err)
		}
		if exists {
			return report, fmt.Errorf("legacy migration refused: %s already exist; database is already migrated or has mixed state", check.name)
		}
	}
	adminExists, err := client.User.Query().Where(user.RoleEQ("admin"), user.ActiveEQ(true)).Exist(ctx)
	if err != nil {
		return report, fmt.Errorf("inspect legacy administrator: %w", err)
	}
	if !adminExists {
		return report, errors.New("legacy migration requires an existing active administrator; restore administrator access with the old version before migrating")
	}
	var localID *int
	if wantMode == instanceinitialization.ModeLocal {
		account, err := client.User.Query().Where(user.UsernameEQ("local")).Only(ctx)
		if err != nil || !account.Active || account.Role != "admin" {
			return report, errors.New("legacy local migration requires the existing active administrator named local; restore the original local account with v0.13.0 before migrating")
		}
		localID = &account.ID
	}
	settings, err := client.SystemSetting.Query().All(ctx)
	if err != nil {
		return report, fmt.Errorf("read legacy settings: %w", err)
	}
	// v0.13.0 treats a missing or empty registration setting as enabled, and
	// treats every other value except the literal "true" as disabled.
	report.RegistrationEnabled = true
	var registration *ent.SystemSetting
	for _, setting := range settings {
		switch setting.Key {
		case "registration_enabled":
			registration = setting
			report.RegistrationEnabled = setting.Value == "" || setting.Value == "true"
		case "default_user_role", "auto_admin":
			// These obsolete defaults do not describe any existing user's role.
		default:
			return Report{}, errors.New("legacy migration refused: unsupported system setting; expected v0.13.0 settings")
		}
	}
	rows, err := client.Backend.Query().All(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("read legacy backends: %w", err)
	}
	plans := make([]legacyBackendMigration, 0, len(rows))
	for _, row := range rows {
		plan, err := prepareLegacyBackend(row)
		if err != nil {
			return Report{}, fmt.Errorf("legacy backend %d: %w", row.ID, err)
		}
		plans = append(plans, plan)
	}

	for _, plan := range plans {
		update := client.Backend.UpdateOneID(plan.row.ID).SetOptions(plan.options).SetUpdatedAt(plan.row.UpdatedAt)
		if plan.input.Secret != "" {
			row, err := credentialstore.Create(ctx, client, keys, plan.input)
			if err != nil {
				return Report{}, fmt.Errorf("encrypt legacy backend %d credential: %w", plan.row.ID, err)
			}
			update.SetCredentialID(row.ID)
			report.CredentialsCreated++
		} else {
			report.BackendsWithoutSecret++
		}
		if plan.hasKey {
			if err := update.Exec(ctx); err != nil {
				return Report{}, fmt.Errorf("update legacy backend %d: %w", plan.row.ID, err)
			}
			report.BackendsMigrated++
		}
	}
	value := strconv.FormatBool(report.RegistrationEnabled)
	if registration == nil {
		err = client.SystemSetting.Create().SetKey("registration_enabled").SetValue(value).Exec(ctx)
	} else if registration.Value != value {
		err = client.SystemSetting.UpdateOneID(registration.ID).SetValue(value).SetUpdatedAt(registration.UpdatedAt).Exec(ctx)
	}
	if err != nil {
		return Report{}, fmt.Errorf("migrate registration policy: %w", err)
	}
	report.SettingsRemoved, err = client.SystemSetting.Delete().Where(systemsetting.KeyIn("default_user_role", "auto_admin")).Exec(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("remove obsolete legacy settings: %w", err)
	}
	if err := client.InstanceInitialization.Create().SetID(1).SetVersion(targetInitializationVersion).SetMode(wantMode).SetNillableLocalUserID(localID).Exec(ctx); err != nil {
		return Report{}, fmt.Errorf("create migrated initialization marker: %w", err)
	}
	if _, err := service.NewInitializationService(client).Validate(ctx, mode); err != nil {
		return Report{}, fmt.Errorf("validate migrated instance: %w", err)
	}
	return report, nil
}

func prepareLegacyBackend(row *ent.Backend) (legacyBackendMigration, error) {
	plan := legacyBackendMigration{row: row, options: cloneOptions(row.Options)}
	if row.CredentialID != nil {
		return plan, errors.New("credential binding already exists; mixed migration state")
	}
	plan.input.Scope, plan.input.Provider = row.Scope, string(row.BackendType)
	switch {
	case row.Scope == "user" && row.OwnerUserID != nil && row.OwnerOrgID == nil:
		plan.input.OwnerID = *row.OwnerUserID
	case row.Scope == "org" && row.OwnerOrgID != nil && row.OwnerUserID == nil:
		plan.input.OwnerID = *row.OwnerOrgID
	default:
		return plan, errors.New("invalid credential owner")
	}
	if plan.input.OwnerID <= 0 || !supportedProvider(plan.input.Provider) {
		return plan, errors.New("invalid credential owner or provider")
	}
	if raw, ok := plan.options["base_url"]; ok {
		var valid bool
		plan.input.Endpoint, valid = raw.(string)
		if !valid {
			return plan, errors.New("base_url must be a string")
		}
	}
	endpoint, err := freezeEndpoint(plan.input.Provider, plan.input.Endpoint)
	if err != nil {
		return plan, errors.New("base_url is not supported by the current credential endpoint policy")
	}
	plan.input.Endpoint = endpoint
	if raw, ok := plan.options["api_key"]; ok {
		plan.hasKey = true
		// JSON null was treated like a missing key by the old provider adapters.
		if raw != nil {
			var valid bool
			plan.input.Secret, valid = raw.(string)
			if !valid {
				return plan, errors.New("api_key must be a string or null")
			}
		}
		delete(plan.options, "api_key")
	}
	return plan, nil
}
