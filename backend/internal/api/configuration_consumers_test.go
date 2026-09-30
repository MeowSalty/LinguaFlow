package api

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segmentrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// Build the production service graph, with one SQLite connection so concurrent
// recovery and request goroutines share the same in-memory database.
func configurationConsumerServer(t *testing.T, mode string, edit func(*config.ServerConfig), address ...config.RuntimeAddress) (*Server, *config.ServerConfig, *ent.User) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.JWTSecret = strings.Repeat("configuration-consumer-key", 2)
	cfg.ShutdownTimeout = 2 * time.Second
	cfg.ServeUI = false
	cfg.Workers = config.WorkerConfig{
		Translation: config.RunnerConfig{Count: 1, QueueCapacity: 2},
		Sync:        config.RunnerConfig{Count: 1, QueueCapacity: 2},
	}
	cfg.Credentials.KeyringFile = filepath.Join(cfg.DataDir, "keyring.json")
	if edit != nil {
		edit(cfg)
	}
	if _, err := credential.PrepareKeyring(cfg.Credentials.KeyringFile, true); err != nil {
		t.Fatal(err)
	}
	bootstrap := config.BootstrapInput{}
	if mode == config.ModeServer {
		bootstrap.Admin = &config.BootstrapAdmin{Username: "consumer-admin", Email: "consumer@test.invalid", Password: "consumer-password"}
	}
	local, err := service.NewInitializationService(client).Initialize(context.Background(), mode, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), db, client, mode, local, address...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	user := client.User.Query().OnlyX(context.Background())
	return s, cfg, user
}

func TestConfigurationConsumersWorkerCapacity(t *testing.T) {
	want := map[string]config.RunnerConfig{
		"translation":   {Count: 2, QueueCapacity: 7},
		"glossary_sync": {Count: 3, QueueCapacity: 11},
	}
	s, _, _ := configurationConsumerServer(t, config.ModeLocal, func(cfg *config.ServerConfig) {
		cfg.Workers.Translation = want["translation"]
		cfg.Workers.Sync = want["glossary_sync"]
	})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- s.dispatcher.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("dispatcher did not stop")
		}
	}()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshots := s.dispatcher.Snapshot()
		ready := len(snapshots) == len(want)
		for _, snapshot := range snapshots {
			capacity, ok := want[snapshot.TaskType]
			if !ok {
				t.Fatalf("unexpected runner %q", snapshot.TaskType)
			}
			if snapshot.QueueCapacity == nil || *snapshot.QueueCapacity != capacity.QueueCapacity {
				t.Fatalf("queue capacity for %s: %+v", snapshot.TaskType, snapshot)
			}
			ready = ready && snapshot.State == "running" && snapshot.WorkerCapacity != nil && *snapshot.WorkerCapacity == capacity.Count && snapshot.WorkersAlive != nil && *snapshot.WorkersAlive == capacity.Count
		}
		if ready {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("configured worker pools did not start: %+v", snapshots)
		case <-ticker.C:
		}
	}
}

func configurationConsumerJob(t *testing.T, s *Server, userID int) *ent.Job {
	t.Helper()
	ctx := context.Background()
	project := s.entClient.Project.Create().SetName("consumer-project").SetSourceLang("en").SetTargetLang("zh").SetOwnerUserID(userID).SaveX(ctx)
	return s.entClient.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SaveX(ctx)
}

func configurationPublish(s *Server, jobID int, message string) {
	s.eventBroker.Publish(jobID, event.Event{Type: "translation", JobID: jobID, Level: "info", Stage: "translate", Message: message, CreatedAt: time.Now()})
}

func TestConfigurationConsumersSSERingCapacity(t *testing.T) {
	for _, capacity := range []int{3, 5} {
		t.Run(strconv.Itoa(capacity), func(t *testing.T) {
			s, _, user := configurationConsumerServer(t, config.ModeLocal, func(cfg *config.ServerConfig) { cfg.SSE.RingBufferCapacity = capacity })
			job := configurationConsumerJob(t, s, user.ID)
			count := capacity + 2
			for i := 0; i < count; i++ {
				configurationPublish(s, job.ID, "ring-event")
			}
			history, _, more := s.eventBroker.ListHistory(context.Background(), job.ID, 0, count+1)
			if len(history) != count || more {
				t.Fatalf("ring eviction lost durable history: %+v", history)
			}
			// Remove only durable rows so the following probes can only be served
			// by the production ring. The exact overflow boundary proves capacity.
			s.entClient.SSEEvent.Delete().ExecX(context.Background())
			oldest := int64(count - capacity + 1)
			if got := s.eventBroker.Replay(context.Background(), job.ID, oldest, count); len(got) != capacity-1 || got[0].Seq != oldest+1 || got[len(got)-1].Seq != int64(count) {
				t.Fatalf("configured ring capacity %d: %+v", capacity, got)
			}
			if got := s.eventBroker.Replay(context.Background(), job.ID, oldest-1, count); len(got) != 0 {
				t.Fatalf("overflow must require durable fallback: %+v", got)
			}
		})
	}
}

type configurationStreamRecorder struct {
	*httptest.ResponseRecorder
	chunks  [][]int64
	offset  int
	onFlush func([]int64)
}

func (w *configurationStreamRecorder) Flush() {
	w.ResponseRecorder.Flush()
	body := w.Body.String()
	ids := parseIDLines(body[w.offset:])
	w.offset = len(body)
	if len(ids) > 0 {
		w.chunks = append(w.chunks, ids)
	}
	if w.onFlush != nil {
		w.onFlush(ids)
	}
}

func TestConfigurationConsumersSSEReplayAndSubscription(t *testing.T) {
	s, _, user := configurationConsumerServer(t, config.ModeLocal, func(cfg *config.ServerConfig) {
		cfg.SSE = config.SSEConfig{RingBufferCapacity: 4, ReplayBatchSize: 2, MaxReplayEvents: 3}
	})
	job := configurationConsumerJob(t, s, user.ID)
	for i := 0; i < 7; i++ {
		configurationPublish(s, job.ID, "history")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w := &configurationStreamRecorder{ResponseRecorder: httptest.NewRecorder()}
	w.onFlush = func(ids []int64) {
		if len(ids) == 0 {
			return
		}
		switch ids[len(ids)-1] {
		case 7:
			configurationPublish(s, job.ID, "live")
		case 8:
			cancel()
		}
	}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/jobs/%d/stream", job.ID), nil).WithContext(ctx)
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", w.Code, w.Body.String())
	}
	if want := [][]int64{{5, 6}, {7}, {8}}; !reflect.DeepEqual(w.chunks, want) {
		t.Fatalf("replay batches and live subscription: got %v want %v", w.chunks, want)
	}
	s.eventBroker.Purge(job.ID)
	history, _, more := s.eventBroker.ListHistory(context.Background(), job.ID, 0, 20)
	if len(history) != 8 || more {
		t.Fatalf("stream window or purge lost durable history: %+v", history)
	}
}

func TestConfigurationConsumersCORS(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		origins []string
	}{
		{name: "serve_empty", mode: config.ModeServer, origins: []string{}},
		{name: "serve_explicit", mode: config.ModeServer, origins: []string{"https://client.example"}},
		{name: "local_actual_port", mode: config.ModeLocal, origins: []string{"https://client.example"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			port := listener.Addr().(*net.TCPAddr).Port
			s, cfg, _ := configurationConsumerServer(t, test.mode, func(cfg *config.ServerConfig) {
				cfg.Port = 0
				cfg.CORS.AllowedOrigins = test.origins
			}, config.RuntimeAddress{Host: "127.0.0.1", Port: port})
			host := httptest.NewUnstartedServer(s.httpServer.Handler)
			_ = host.Listener.Close()
			host.Listener = listener
			host.Start()
			defer host.Close()
			origins := []string{"https://client.example", "https://unlisted.example", "http://localhost:0", "http://127.0.0.1:0", "http://[::1]:0"}
			for _, local := range []string{"localhost", "127.0.0.1", "[::1]"} {
				origins = append(origins, "http://"+local+":"+strconv.Itoa(port))
			}
			for _, origin := range origins {
				allowed := test.name == "serve_explicit" && origin == "https://client.example" || test.mode == config.ModeLocal && strings.HasSuffix(origin, ":"+strconv.Itoa(port))
				for _, method := range []string{http.MethodGet, http.MethodOptions} {
					req, err := http.NewRequest(method, host.URL+"/health", nil)
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Origin", origin)
					if method == http.MethodOptions {
						req.Header.Set("Access-Control-Request-Method", http.MethodGet)
					}
					resp, err := host.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					_ = resp.Body.Close()
					want := ""
					if allowed {
						want = origin
					}
					if got := resp.Header.Get("Access-Control-Allow-Origin"); got != want {
						t.Errorf("%s Origin %s: allow-origin %q want %q", method, origin, got, want)
					}
				}
			}
			if cfg.Port != 0 {
				t.Fatalf("runtime binding mutated configured port: %d", cfg.Port)
			}
		})
	}
}

func TestConfigurationConsumersRevisionRetention(t *testing.T) {
	s, _, user := configurationConsumerServer(t, config.ModeLocal, func(cfg *config.ServerConfig) { cfg.RevisionRetention = time.Hour })
	ctx := context.Background()
	project := s.entClient.Project.Create().SetName("revision-project").SetSourceLang("en").SetTargetLang("zh").SetOwnerUserID(user.ID).SaveX(ctx)
	resource := s.entClient.Resource.Create().SetProjectID(project.ID).SetPath("chapter.txt").SetFormat("txt").SetStoragePath("chapter.txt").SaveX(ctx)
	seg := s.entClient.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("source").SetTargetText("old target").SetStatus(segment.StatusTranslated).SaveX(ctx)
	revision := func(name string, created time.Time) *ent.SegmentRevision {
		return s.entClient.SegmentRevision.Create().SetSegmentID(seg.ID).SetResourceID(resource.ID).SetOperationID(name).
			SetKind(segmentrevision.KindReplace).SetBeforeStatus(segmentrevision.BeforeStatusTranslated).SetAfterStatus(segmentrevision.AfterStatusEdited).
			SetActorID(user.ID).SetCreatedAt(created).SaveX(ctx)
	}
	old := revision("expired", time.Now().Add(-2*time.Hour))
	recent := revision("retained", time.Now().Add(-30*time.Minute))
	result, err := s.segmentSvc.ApplySearchReplace(ctx, user.ID, project.ID, resource.ID, service.SearchReplaceOptions{Find: "old", ReplaceWith: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AppliedCount != 1 {
		t.Fatalf("replacement did not trigger retention: %+v", result)
	}
	if s.entClient.SegmentRevision.Query().Where(segmentrevision.IDEQ(old.ID)).ExistX(ctx) {
		t.Fatal("revision older than configured retention survived")
	}
	if !s.entClient.SegmentRevision.Query().Where(segmentrevision.IDEQ(recent.ID)).ExistX(ctx) {
		t.Fatal("recent revision was removed")
	}
}

func TestConfigurationConsumersQuickTranslateTimeoutAndConcurrency(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)
	s, cfg, user := configurationConsumerServer(t, config.ModeLocal, func(cfg *config.ServerConfig) {
		cfg.QuickTranslate.MaxConcurrency = 1
		cfg.QuickTranslate.Timeout = 500 * time.Millisecond
	})
	secret := "test-provider-secret"
	provider, err := s.backendSvc.Create(context.Background(), service.CreateBackendInput{
		Scope: service.ScopeUser, OwnerUserID: &user.ID,
		BackendInput: service.BackendInput{Name: "slow-provider", Type: "openai", Options: map[string]any{"api_key": secret, "base_url": upstream.URL, "model": "test", "timeout": 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileConfig := schema.DefaultProfileConfig()
	profileConfig.Ruby.Enabled = false
	profileConfig.Repair.Enabled = false
	profile, err := s.executionProfileSvc.Create(context.Background(), user.ID, service.CreateExecutionProfileInput{Name: "timeout-profile", Config: &profileConfig})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.executionPlanSvc.Create(context.Background(), user.ID, service.CreateExecutionPlanTemplateInput{
		Name: "timeout-plan", ProfileID: profile.ID,
		Rounds: []schema.ExecutionRoundConfig{{Mode: "translate", BackendID: provider.ID, Translate: &schema.TranslateRoundConfig{
			PromptTemplateID: -1, BatchSize: 1, MaxWordsPerBatch: 500, Concurrency: 1, FallbackShrink: 1,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"source_text":"Hello","source_lang":"en","target_lang":"zh","execution_plan_id":%d}`, plan.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/quick-translate", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)
		return w
	}
	finished := make(chan *httptest.ResponseRecorder, 1)
	started := time.Now()
	go func() { finished <- request() }()
	select {
	case <-entered:
	case w := <-finished:
		t.Fatalf("request did not reach provider: %d %s", w.Code, w.Body.String())
	case <-time.After(3 * time.Second):
		t.Fatal("provider was never called")
	}
	busy := request()
	if busy.Code != http.StatusServiceUnavailable || busy.Header().Get("Retry-After") != "1" {
		t.Errorf("configured actor concurrency: %d %s", busy.Code, busy.Body.String())
	}
	select {
	case w := <-finished:
		if w.Code != http.StatusGatewayTimeout {
			t.Fatalf("configured request timeout: %d %s", w.Code, w.Body.String())
		}
		if elapsed := time.Since(started); elapsed < cfg.QuickTranslate.Timeout || elapsed > 2*time.Second {
			t.Fatalf("configured timeout %s completed after %s", cfg.QuickTranslate.Timeout, elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request ignored configured timeout")
	}
}
