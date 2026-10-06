// Package v013 implements only the v0.13.0 one-time database conversion.
// Normal service startup must not import this package.
package v013

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const (
	sourceVersion               = "v0.13.0"
	targetSchemaVersion         = 1
	targetDefaultsVersion       = 1
	targetInitializationVersion = 1
	targetCredentialVersion     = 1
	targetSQLiteVersion         = 1
)

func checkTargetCompatibility() error {
	if execution.SchemaVersion != targetSchemaVersion || execution.DefaultsVersion != targetDefaultsVersion || service.InitializationVersion != targetInitializationVersion {
		return errors.New("migration target format changed; explicitly update the v0.13.0 converter and compatibility fixtures before using it")
	}
	return nil
}

// Convert migrates a schema-upgraded database. The caller owns the transaction
// or disposable database copy and must discard all changes on any error.
func Convert(ctx context.Context, client *ent.Client, keys *credential.Keyring, mode string) (Report, error) {
	if err := checkTargetCompatibility(); err != nil {
		return Report{}, err
	}
	report, err := migrateInstance(ctx, client, keys, mode)
	if err != nil {
		return Report{}, err
	}
	executions, err := migrateExecution(ctx, client, keys)
	if err != nil {
		return Report{}, err
	}
	report.Profiles, report.Jobs = executions.Profiles, executions.Jobs
	report.CredentialsCreated += executions.Credentials
	// Fail rather than silently writing a future encryption format. Validating
	// the active key beforehand is insufficient when the writer format changes.
	versions, err := client.CredentialVersion.Query().All(ctx)
	if err != nil {
		return Report{}, err
	}
	for _, row := range versions {
		if row.EncryptionVersion != targetCredentialVersion {
			return Report{}, errors.New("unsupported migrated credential format")
		}
	}
	if err := service.NewCredentialService(client, keys, nil).ValidateKeys(ctx); err != nil {
		return Report{}, fmt.Errorf("validate migrated credentials: %w", err)
	}
	auditMode := mode
	if auditMode == "server" {
		auditMode = "serve"
	}
	if err := client.ActivityLog.Create().SetVisibilityScope(activitylog.VisibilityScopeUnknown).
		SetAction("instance.migrate_legacy").SetResourceType("instance").SetResourceID(1).
		SetMetadata(map[string]any{"source_version": sourceVersion, "mode": auditMode,
			"execution_schema_version": targetSchemaVersion, "execution_defaults_version": targetDefaultsVersion,
			"initialization_version": targetInitializationVersion, "credential_format_version": targetCredentialVersion,
			"credentials_created": report.CredentialsCreated, "profiles": report.Profiles, "jobs": report.Jobs,
			"settings_removed": report.SettingsRemoved, "registration_enabled": report.RegistrationEnabled}).Exec(ctx); err != nil {
		return Report{}, fmt.Errorf("record legacy migration: %w", err)
	}
	return report, nil
}

func supportedProvider(provider string) bool {
	return provider == "openai" || provider == "anthropic" || provider == "google"
}

func cloneOptions(options map[string]any) map[string]any {
	out := make(map[string]any, len(options))
	for key, value := range options {
		out[key] = value
	}
	return out
}
