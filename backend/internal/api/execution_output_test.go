package api

import (
	"context"
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type budgetOutputWriter struct {
	*httptest.ResponseRecorder
	t       *testing.T
	root    *resourcebudget.RootBudget
	written bool
}

type controlOutputWriter struct{ *budgetOutputWriter }

func (w *controlOutputWriter) Write(data []byte) (int, error) {
	if w.Code != http.StatusOK {
		return w.ResponseRecorder.Write(data)
	}
	if v := w.root.Snapshot(7).Instance; v.QueuedDescriptors <= 0 {
		w.t.Fatalf("control result retired before HTTP Write: %+v", v)
	}
	return w.budgetOutputWriter.Write(data)
}

func TestBudgetIntegrationHTTPControlResultEndsAfterWrite(t *testing.T) {
	s, _, u := configurationConsumerServer(t, config.ModeLocal, nil)
	ctx := context.Background()
	profile := execution.DefaultProfile()
	input := execution.JobExecutionSnapshot{SourceLang: "en", TargetLang: "zh", RubyTemplates: execution.RubyTemplates{JSON: "json", Text: "text", BatchJSON: "batch json", BatchText: "batch text"}, Strategy: execution.StrategySnapshot{Protect: profile.Protect, Postprocess: profile.Postprocess, Repair: profile.Repair, Context: profile.Context, Ruby: profile.Ruby, QA: profile.QA}, Rounds: []execution.JobRoundSnapshot{{Mode: "correct", Correct: &execution.JobCorrectRoundSnapshot{Concurrency: 1}}}}
	spec, err := execution.Resolve(input)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var frozen map[string]any
	if err = json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	p := s.entClient.Project.Create().SetName("control").SetOwnerUserID(u.ID).SaveX(ctx)
	resource := s.entClient.Resource.Create().SetProjectID(p.ID).SetPath("control.txt").SetStoragePath("control.txt").SetFormat("txt").SaveX(ctx)
	job := s.entClient.Job.Create().SetProjectID(p.ID).SetCreatedByID(u.ID).SetExecutionPlanID(1).SetExecutionConfig(frozen).SetStatus(service.JobStatusPaused).SaveX(ctx)
	jr := s.entClient.JobResource.Create().SetJobID(job.ID).SetResourceID(resource.ID).SetStatus(service.JobResourceStatusRunning).SaveX(ctx)
	s.entClient.JobRound.Create().SetJobID(job.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("correct").SetStatus(service.JobRoundStatusRunning).SaveX(ctx)
	req := httptest.NewRequest(http.MethodPost, "/resume", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("jobId", strconv.Itoa(job.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	req = withAuthUser(req, u)
	writer := &controlOutputWriter{&budgetOutputWriter{ResponseRecorder: httptest.NewRecorder(), t: t, root: s.executionBudget}}
	s.handleResumeJob(writer, req)
	if writer.Code != http.StatusOK || !writer.written {
		t.Fatalf("resume HTTP %d %s", writer.Code, writer.Body.String())
	}
	if v := s.executionBudget.Snapshot(u.ID).Instance; v != (resourcebudget.Vector{}) {
		t.Fatalf("control result retained: %+v", v)
	}
}

func (w *budgetOutputWriter) Write(data []byte) (int, error) {
	v := w.root.Snapshot(7).Instance
	if v.BuildWorkers != 0 || v.PayloadBytes <= 0 {
		w.t.Fatalf("encoder CPU/payload ownership at actual Write: %+v", v)
	}
	w.written = true
	return w.ResponseRecorder.Write(data)
}

func TestBudgetIntegrationHTTPFrameOwnershipEndsAfterWrite(t *testing.T) {
	root, err := resourcebudget.New(testServerConfig().ExecutionBudget)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{executionBudget: root}
	w := &budgetOutputWriter{ResponseRecorder: httptest.NewRecorder(), t: t, root: root}
	r := httptest.NewRequest(http.MethodGet, "/frame", nil)
	r = r.WithContext(service.WithExecutionActor(r.Context(), 7))
	s.writeExecutionJSON(w, r, http.StatusOK, map[string]any{"text": "中文 <ruby> & encoded output", "items": []int{1, 2, 3}})
	if !w.written || w.Code != http.StatusOK {
		t.Fatalf("frame was not written: %d %s", w.Code, w.Body.String())
	}
	var decoded map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["text"] != "中文 <ruby> & encoded output" {
		t.Fatal("bounded encoder changed escaped text")
	}
	if got := root.Snapshot(7).Instance; got != (resourcebudget.Vector{}) {
		t.Fatalf("frame lease retained after Write: %+v", got)
	}
}
