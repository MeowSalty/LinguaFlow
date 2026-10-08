package v013

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credentialstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialjobreference"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/executionprofile"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

// executionReport counts converted records; mixed current data is rejected.
type executionReport struct {
	Profiles    int
	Jobs        int
	Credentials int
}

// migrateExecution upgrades v0.13.0 execution data inside the caller's
// transaction, after schema and backend credentials have been migrated. The
// server must be stopped. Errors require rolling back the entire transaction.
func migrateExecution(ctx context.Context, txClient *ent.Client, keys *credential.Keyring) (executionReport, error) {
	var report executionReport
	if keys == nil {
		return report, credential.ErrKeyUnavailable
	}
	if err := migrateLegacyProfiles(ctx, txClient, &report); err != nil {
		return report, err
	}
	m := legacyExecutionMigrator{client: txClient, keys: keys, bindings: make(map[legacySnapshotCredential]credential.Binding)}
	const pageSize = 128
	afterID := 0
	for {
		rows, err := txClient.Job.Query().Where(job.IDGT(afterID)).Order(ent.Asc(job.FieldID)).Limit(pageSize).All(ctx)
		if err != nil {
			return report, fmt.Errorf("read legacy jobs: %w", err)
		}
		for _, row := range rows {
			afterID = row.ID
			changed, err := m.migrateJob(ctx, row)
			if err != nil {
				return report, fmt.Errorf("migrate job %d execution: %w", row.ID, err)
			}
			if changed {
				report.Jobs++
			}
		}
		if len(rows) < pageSize {
			break
		}
	}
	report.Credentials = len(m.bindings)
	return report, nil
}

func migrateLegacyProfiles(ctx context.Context, client *ent.Client, report *executionReport) error {
	const pageSize = 128
	afterID := 0
	for {
		rows, err := client.ExecutionProfile.Query().Where(executionprofile.IDGT(afterID)).Order(ent.Asc(executionprofile.FieldID)).Limit(pageSize).All(ctx)
		if err != nil {
			return fmt.Errorf("read legacy profiles: %w", err)
		}
		for _, row := range rows {
			afterID = row.ID
			raw, err := client.ExecutionProfile.Query().Where(executionprofile.IDEQ(row.ID)).Select(executionprofile.FieldConfig).String(ctx)
			if err != nil {
				return fmt.Errorf("read legacy profile %d: %w", row.ID, err)
			}
			cfg, err := convertProfile([]byte(raw))
			if err != nil {
				return fmt.Errorf("legacy profile %d: %w", row.ID, err)
			}
			if err := client.ExecutionProfile.UpdateOneID(row.ID).SetConfig(cfg).SetUpdatedAt(row.UpdatedAt).Exec(ctx); err != nil {
				return fmt.Errorf("save legacy profile %d: %w", row.ID, err)
			}
			report.Profiles++
		}
		if len(rows) < pageSize {
			return nil
		}
	}
}

type legacySnapshotCredential struct {
	Scope    string
	OwnerID  int
	Provider string
	Endpoint string
	Digest   [32]byte
}

type legacyExecutionMigrator struct {
	client   *ent.Client
	keys     *credential.Keyring
	bindings map[legacySnapshotCredential]credential.Binding
}

func (m *legacyExecutionMigrator) migrateJob(ctx context.Context, row *ent.Job) (bool, error) {
	if len(row.ExecutionConfig) == 0 {
		return false, errors.New("missing execution snapshot; recreate or repair this job before migrating")
	}
	raw, err := json.Marshal(row.ExecutionConfig)
	if err != nil {
		return false, errors.New("cannot encode execution snapshot")
	}
	var old sourceJobExecutionSnapshot
	if err := decodeSource(raw, &old); err != nil {
		return false, err
	}
	if old.SourceLang == "" || old.TargetLang == "" || len(old.Rounds) == 0 {
		return false, errors.New("legacy snapshot requires saved languages and rounds")
	}
	// Decode a validated, version-fixed source before mapping to target v1.
	// The removed glossary strategy is discarded; translation rounds start with
	// inline term extraction disabled instead of inheriting that legacy toggle.
	var snapshot execution.JobExecutionSnapshot
	if err := transcode(old, &snapshot); err != nil {
		return false, err
	}
	snapshot.RubyTemplates = execution.RubyTemplates{JSON: sourceTemplate(rubyJSONTemplate), Text: sourceTemplate(rubyTextTemplate)}
	for i := range snapshot.Rounds {
		round := &snapshot.Rounds[i]
		if round.Mode == "correct" {
			if legacySnapshotHasBackend(round.Backend) {
				return false, fmt.Errorf("round %d: local correction unexpectedly contains a backend", i)
			}
		} else {
			if err := m.migrateBackend(ctx, row, &round.Backend); err != nil {
				return false, fmt.Errorf("round %d: %w", i, err)
			}
		}
		switch round.Mode {
		case "adjudicate":
			if round.Adjudicate != nil {
				round.Adjudicate.TemplateContent = sourceTemplate(adjudicationTemplate)
			}
		case "semantic_qa":
			if round.SemanticQA != nil {
				round.SemanticQA.TemplateContent = sourceTemplate(semanticQATemplate)
			}
		case "revise":
			if round.Revise != nil {
				round.Revise.TemplateContent = sourceTemplate(reviseTemplate)
			}
		}
	}
	if snapshot.RubyRetry != nil && snapshot.RubyRetry.Enabled {
		if err := m.migrateBackend(ctx, row, &snapshot.RubyRetry.Backend); err != nil {
			return false, fmt.Errorf("ruby retry: %w", err)
		}
		if snapshot.RubyRetry.MaxAttempts <= 0 {
			snapshot.RubyRetry.MaxAttempts = 1
		}
	} else if snapshot.RubyRetry != nil && legacySnapshotHasBackend(snapshot.RubyRetry.Backend) {
		return false, errors.New("disabled legacy ruby retry unexpectedly contains a backend")
	}
	resolved, err := freezeExecution(snapshot)
	if err != nil {
		return false, err
	}
	raw, err = json.Marshal(resolved)
	if err != nil {
		return false, errors.New("cannot encode migrated execution snapshot")
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return false, errors.New("cannot materialize migrated execution snapshot")
	}
	if err := m.client.Job.UpdateOneID(row.ID).SetExecutionConfig(config).SetUpdatedAt(row.UpdatedAt).Exec(ctx); err != nil {
		return false, err
	}
	if err := m.retainBindings(ctx, row.ID, resolved.Bindings()); err != nil {
		return false, err
	}
	return true, nil
}

func legacySnapshotHasBackend(snapshot execution.BackendSnapshot) bool {
	return snapshot.ID != 0 || snapshot.Scope != "" || snapshot.Name != "" || snapshot.Type != "" || snapshot.RateLimitPerMinute != 0 || len(snapshot.Options) != 0 || snapshot.Credential != (credential.Binding{})
}

func (m *legacyExecutionMigrator) migrateBackend(ctx context.Context, jobRow *ent.Job, snapshot *execution.BackendSnapshot) error {
	if snapshot.ID <= 0 || snapshot.Credential != (credential.Binding{}) {
		return errors.New("legacy backend identity or credential format is invalid")
	}
	secret, ok := snapshot.Options["api_key"].(string)
	if !ok || secret == "" {
		return errors.New("legacy backend snapshot has no API key; cannot substitute a current credential for historical execution")
	}
	delete(snapshot.Options, "api_key")
	opts, err := freezeBackendOptions(snapshot.Type, snapshot.Options)
	if err != nil {
		return err
	}
	snapshot.Options = opts
	scope, ownerID, err := m.backendOwner(ctx, jobRow, snapshot)
	if err != nil {
		return err
	}
	key := legacySnapshotCredential{Scope: scope, OwnerID: ownerID, Provider: snapshot.Type, Endpoint: opts["base_url"].(string), Digest: sha256.Sum256([]byte(secret))}
	if binding, ok := m.bindings[key]; ok {
		snapshot.Credential = binding
		return nil
	}
	// Independent credentials preserve the exact key and endpoint even when the
	// backend was edited between job creation and this one-time migration.
	row, err := credentialstore.Create(ctx, m.client, m.keys, credentialstore.CreateInput{Scope: scope, OwnerID: ownerID, Provider: snapshot.Type, Endpoint: key.Endpoint, Secret: secret})
	if err != nil {
		return err
	}
	snapshot.Credential = credential.Binding{ID: row.ID, Version: row.CurrentVersion}
	m.bindings[key] = snapshot.Credential
	return nil
}

func (m *legacyExecutionMigrator) backendOwner(ctx context.Context, jobRow *ent.Job, snapshot *execution.BackendSnapshot) (string, int, error) {
	if snapshot.Scope != "user" && snapshot.Scope != "org" {
		return "", 0, errors.New("legacy backend snapshot has invalid ownership scope")
	}
	backend, err := m.client.Backend.Get(ctx, snapshot.ID)
	if err == nil {
		if backend.Scope != snapshot.Scope {
			return "", 0, errors.New("legacy backend snapshot scope differs from the existing backend")
		}
		if backend.Scope == "user" && backend.OwnerUserID != nil && backend.OwnerOrgID == nil {
			return "user", *backend.OwnerUserID, nil
		}
		if backend.Scope == "org" && backend.OwnerOrgID != nil && backend.OwnerUserID == nil {
			return "org", *backend.OwnerOrgID, nil
		}
		return "", 0, errors.New("legacy backend has invalid ownership")
	}
	if !ent.IsNotFound(err) {
		return "", 0, err
	}
	// Deleted backends remain deleted: historical jobs stay readable, but the
	// normal execution policy still refuses to restart a deleted backend.
	project, err := m.client.Project.Get(ctx, jobRow.ProjectID)
	if err != nil {
		return "", 0, err
	}
	if project.OwnerUserID != nil && project.OwnerOrgID == nil {
		if snapshot.Scope != "user" {
			return "", 0, errors.New("deleted backend snapshot scope differs from the project owner; original credential owner cannot be determined")
		}
		return "user", *project.OwnerUserID, nil
	}
	if project.OwnerOrgID != nil && project.OwnerUserID == nil {
		if snapshot.Scope != "org" {
			return "", 0, errors.New("deleted backend snapshot scope differs from the project owner; original credential owner cannot be determined")
		}
		return "org", *project.OwnerOrgID, nil
	}
	return "", 0, errors.New("deleted backend snapshot has no unambiguous project owner")
}

func (m *legacyExecutionMigrator) retainBindings(ctx context.Context, jobID int, bindings []credential.Binding) error {
	for _, binding := range bindings {
		version, err := m.client.CredentialVersion.Query().Where(credentialversion.CredentialIDEQ(binding.ID), credentialversion.VersionEQ(binding.Version)).Only(ctx)
		if err != nil {
			return err
		}
		exists, err := m.client.CredentialJobReference.Query().Where(credentialjobreference.JobIDEQ(jobID), credentialjobreference.CredentialVersionIDEQ(version.ID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			if err := m.client.CredentialJobReference.Create().SetJobID(jobID).SetCredentialVersionID(version.ID).Exec(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
