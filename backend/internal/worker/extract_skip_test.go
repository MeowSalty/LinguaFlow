package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

// Extraction surrounds translation so a skipped final round cannot erase its result.
func disabledGlossarySnapshot(t *testing.T, endpoint string) *service.JobExecutionSnapshot {
	t.Helper()
	snapshot := translateSnapshot(t)
	inline := execution.DefaultInlineTermExtraction()
	inline.Enabled = true
	snapshot.Rounds[0].Translate.InlineTermExtraction = &inline
	extract := service.JobRoundSnapshot{
		Mode: "extract", Backend: snapshot.Rounds[0].Backend,
		Extract: &service.JobExtractRoundSnapshot{
			TemplateContent: templates.EmbeddedBootstrapTemplate(), BatchSize: 10,
			Concurrency: 1, MaxTermsPer1000Chars: 3, MinSourceLen: 2,
		},
	}
	snapshot.Rounds = []service.JobRoundSnapshot{extract, snapshot.Rounds[0], extract}
	for i := range snapshot.Rounds {
		round := &snapshot.Rounds[i]
		opts, err := execution.ResolveBackendOptions("openai", map[string]any{
			"base_url": endpoint, "model": round.Mode, "response_format": "none",
		})
		if err != nil {
			t.Fatal(err)
		}
		round.Backend.Options = opts
	}
	return completeWorkerSnapshot(t, snapshot)
}

func extractSkipUpstream(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
		}
		if request.Model != "translate" || strings.Contains(string(body), "附加任务") {
			t.Errorf("disabled extraction reached model: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "skip-test", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": `{"translations":{"1":"你好"}}`}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
		})
	}))
	t.Cleanup(server.Close)
	return server.URL, calls
}

func assertExtractSkippedSummaries(t *testing.T, status, target string, rounds []service.PreviewRoundSummary, warnings []string, calls int64) {
	t.Helper()
	if status != "success" || target != "你好" || len(warnings) != 0 || calls != 1 {
		t.Fatalf("status=%s target=%s warnings=%v calls=%d", status, target, warnings, calls)
	}
	if len(rounds) != 3 || rounds[0].Status != "skipped" || rounds[1].Status != "success" || rounds[2].Status != "skipped" {
		t.Fatalf("rounds=%+v", rounds)
	}
	for i, round := range rounds {
		if round.Index != i {
			t.Fatalf("round index lost: %+v", rounds)
		}
	}
}

func TestDisabledGlossaryQuickTranslationSkipsExtract(t *testing.T) {
	endpoint, calls := extractSkipUpstream(t)
	runner := NewQuickTranslateRunner(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	runner.SetCredentials(fixtureCredentials{}, fixtureCredentials{})
	result, err := runner.Run(context.Background(), service.QuickTranslateRunnerInput{
		Snapshot: disabledGlossarySnapshot(t, endpoint), SourceLang: "en", TargetLang: "zh",
		SourceText: "hello", Glossary: glossary.Nop{}, Format: "txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertExtractSkippedSummaries(t, result.Status, result.TargetText, result.RoundSummary, result.Warnings, calls.Load())
}

func TestDisabledGlossaryPreviewSkipsExtract(t *testing.T) {
	endpoint, calls := extractSkipUpstream(t)
	runner := NewPreviewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	runner.SetCredentials(fixtureCredentials{}, fixtureCredentials{})
	result, err := runner.RunPreview(context.Background(), disabledGlossarySnapshot(t, endpoint),
		&ent.Project{SourceLang: "en", TargetLang: "zh"}, &ent.Resource{ID: 1, Format: "txt"},
		[]*ent.Segment{{ID: 1, SegmentIndex: 0, SourceText: "hello", Status: segment.StatusPending}}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	assertExtractSkippedSummaries(t, result.Status, result.TargetText, result.RoundSummary, result.Warnings, calls.Load())
}

func TestDisabledGlossaryJobSkipsExtractWithoutAddingProgress(t *testing.T) {
	ctx := context.Background()
	endpoint, calls := extractSkipUpstream(t)
	client := newRegistryTestClient(t)
	jobID, resourceJobID, _ := registryFixture(t, client)
	item, err := client.JobResource.Query().Where(jobresource.IDEQ(resourceJobID)).WithResource().Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item, err = client.JobResource.UpdateOne(item).SetSegmentCount(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The update result does not retain loaded edges.
	item, err = client.JobResource.Query().Where(jobresource.IDEQ(resourceJobID)).WithResource().Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seg, err := client.Segment.Create().SetResourceID(item.Edges.Resource.ID).
		SetSegmentIndex(0).SetSourceText("hello").SetStatus(segment.StatusPending).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.Project.Get(ctx, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := loadJobRounds(ctx, client, jobID)
	if err != nil {
		t.Fatal(err)
	}
	runner := &JobRunner{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), client: client,
		jobs: service.NewJobService(client, nil, nil, nil, nil, nil, nil, nil, nil),
	}
	runner.SetCredentials(fixtureCredentials{}, fixtureCredentials{})
	if err := runner.processJobResource(ctx, &service.JobExecution{Job: job, Project: project},
		disabledGlossarySnapshot(t, endpoint), item, registry, nil, nil); err != nil {
		t.Fatal(err)
	}
	item, err = client.JobResource.Get(ctx, resourceJobID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "completed" || item.CompletedSegments != 1 || calls.Load() != 1 {
		t.Fatalf("resource=%+v calls=%d", item, calls.Load())
	}
	translated, err := client.Segment.Get(ctx, seg.ID)
	if err != nil || translated.TargetText == nil || *translated.TargetText != "你好" {
		t.Fatalf("translated=%+v err=%v", translated, err)
	}
	rounds, err := client.JobRound.Query().Where(jobround.JobResourceIDEQ(resourceJobID)).Order(ent.Asc(jobround.FieldRoundIndex)).All(ctx)
	if err != nil || len(rounds) != 3 {
		t.Fatalf("rounds=%+v err=%v", rounds, err)
	}
	for _, index := range []int{0, 2} {
		round := rounds[index]
		if round.Status != "skipped" || round.StartedAt != nil || round.FinishedAt == nil || round.SegmentTotal != 0 || round.SegmentCompleted != 0 {
			t.Fatalf("disabled extraction started work: %+v", round)
		}
	}
	job, err = client.Job.Get(ctx, jobID)
	if err != nil || job.ProgressTotal != 1 || job.ProgressCompleted != 1 {
		t.Fatalf("job progress=%+v err=%v", job, err)
	}
}
