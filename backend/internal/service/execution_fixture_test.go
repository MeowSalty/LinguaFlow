package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialjobreference"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func newExecutionTestBackendService(t *testing.T, client *ent.Client, users *UserService) *BackendService {
	t.Helper()
	svc := NewBackendService(client, users, nil)
	svc.SetCredentials(NewCredentialService(client, credentialTestKeyring(t, "execution", "execution"), users))
	return svc
}

// freezeExecutionTestJob persists a complete, executable snapshot before a state
// machine test resumes/retries a job. Its round modes follow the seeded matrix.
func freezeExecutionTestJob(t *testing.T, client *ent.Client, job *ent.Job) {
	t.Helper()
	ctx := context.Background()
	project := client.Project.GetX(ctx, job.ProjectID)
	if project.OwnerUserID == nil {
		t.Fatal("execution fixture requires a user-owned project")
	}
	ownerID := *project.OwnerUserID
	backendName := fmt.Sprintf("execution-job-%d", job.ID)
	backendRow, err := client.Backend.Query().Where(entbackend.NameEQ(backendName), entbackend.OwnerUserIDEQ(ownerID)).Only(ctx)
	if ent.IsNotFound(err) {
		backendRow = client.Backend.Create().SetName(backendName).SetScope(ScopeUser).SetOwnerUserID(ownerID).SetBackendType("openai").SaveX(ctx)
		backendRow = bindExecutionTestBackend(t, client, backendRow)
	} else if err != nil {
		t.Fatal(err)
	}
	backendID := backendRow.ID
	users := NewUserService(client, nil)
	backends := newExecutionTestBackendService(t, client, users)
	jobs := NewJobService(client, NewProjectService(client, users), nil, backends, NewTranslationPromptTemplateService(client), NewBootstrapPromptTemplateService(client), NewExecutionProfileService(client, users), nil, nil)
	rows := client.JobRound.Query().Where(jobround.JobIDEQ(job.ID)).AllX(ctx)
	modes := map[int]string{0: "translate"}
	last := 0
	for _, row := range rows {
		modes[row.RoundIndex] = row.Mode
		if row.RoundIndex > last {
			last = row.RoundIndex
		}
	}
	rounds := make([]schema.ExecutionRoundConfig, last+1)
	for i := range rounds {
		round := schema.ExecutionRoundConfig{Mode: modes[i], BackendID: backendID}
		switch round.Mode {
		case "", "translate":
			round = validTranslateRound(backendID)
		case "extract":
			round.Extract = &schema.ExtractRoundConfig{BootstrapTemplateID: -1, BatchSize: 10, Concurrency: 1}
		case "adjudicate":
			round.Adjudicate = &schema.AdjudicateRoundConfig{BatchSize: 10, Concurrency: 1}
		case "semantic_qa":
			round.SemanticQA = &schema.SemanticQARoundConfig{BatchSize: 10, Concurrency: 1}
		case "revise":
			round.Revise = &schema.ReviseRoundConfig{BatchSize: 10, Concurrency: 1}
		case "correct":
			round.BackendID = 0
			round.Correct = &schema.CorrectRoundConfig{Concurrency: 1}
		default:
			t.Fatalf("unsupported fixture round mode %q", round.Mode)
		}
		rounds[i] = round
	}
	plan := &ent.ExecutionPlanTemplate{ID: job.ExecutionPlanID, Name: "state-machine-plan", SchemaVersion: 1, ProfileID: -1, Rounds: rounds}
	snapshot, err := jobs.validateAndSnapshotWith(ctx, ownerID, plan, "", func(int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	snapshot.SourceLang, snapshot.TargetLang = project.SourceLang, project.TargetLang
	resolved, err := execution.Resolve(*snapshot)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.Job.UpdateOneID(job.ID).SetExecutionConfig(config).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.CredentialJobReference.Delete().Where(credentialjobreference.JobIDEQ(job.ID)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := backends.Credentials().RetainJob(ctx, tx, job.ID, resolved.Bindings()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	job.ExecutionConfig = config
}

// bindExecutionTestBackend equips deliberately seeded ownership fixtures with a
// real encrypted credential. Only setup bypasses authorization; execution still
// uses the production binding, decryption and scope checks.
func bindExecutionTestBackend(t *testing.T, client *ent.Client, row *ent.Backend) *ent.Backend {
	t.Helper()
	ctx := context.Background()
	options := make(map[string]any, len(row.Options)+1)
	for key, value := range row.Options {
		options[key] = value
	}
	secret, _ := options["api_key"].(string)
	if secret == "" {
		secret = "execution-test-secret"
	}
	delete(options, "api_key")
	if model, _ := options["model"].(string); model == "" {
		options["model"] = "test-model"
	}
	options, err := execution.ResolveBackendOptions(string(row.BackendType), options)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := 0
	if row.OwnerUserID != nil {
		ownerID = *row.OwnerUserID
	}
	if row.OwnerOrgID != nil {
		ownerID = *row.OwnerOrgID
	}
	credentials := NewCredentialService(client, credentialTestKeyring(t, "execution", "execution"), nil)
	credentials.mu.Lock()
	defer credentials.mu.Unlock()
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	key, err := credentials.createWith(ctx, tx.Client(), CreateCredentialInput{
		Scope: row.Scope, OwnerID: ownerID, Provider: string(row.BackendType), Endpoint: options["base_url"].(string), Secret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := tx.Backend.UpdateOneID(row.ID).SetCredentialID(key.ID).SetOptions(options).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return updated.Unwrap()
}
