package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/worker"
)

func TestRuntimeAndMetricsRequireCurrentSystemAdmin(t *testing.T) {
	s, c, u := authTestServer(t)
	ctx := context.Background()
	s.collector = telemetry.NewCollector()
	s.limiterPool = backend.NewLimiterPool()
	s.limiterPool.Initialize(map[int]int{})
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	org := c.Organization.Create().SetName("runtime-org").SetSlug("runtime-org").SaveX(ctx)
	c.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(u.ID).SetRole("owner").SaveX(ctx)
	for _, path := range []string{"/api/v1/admin/runtime/summary", "/metrics"} {
		if got := jobQueryRequest(router, path, ""); got.Code != 401 {
			t.Fatal(path, got.Code)
		}
		if got := jobQueryRequest(router, path, token); got.Code != 403 {
			t.Fatal(path, got.Code)
		}
	}
	c.User.UpdateOne(u).SetRole(service.SystemRoleAdmin).ExecX(ctx)
	w := jobQueryRequest(router, "/api/v1/admin/runtime/summary", token)
	assertP4ResponseSchema(t, "/admin/runtime/summary", w)
	snapshot := jobQueryDecode[RuntimeSummary](t, w)
	if w.Header().Get("Cache-Control") != "no-store" || snapshot.Scope != "instance" || snapshot.InstanceId == "" || len(snapshot.ExternalRequests) != 8 {
		t.Fatalf("runtime=%+v headers=%v", snapshot, w.Header())
	}
	if got := jobQueryRequest(router, "/metrics", token); got.Code != 200 || !strings.Contains(got.Body.String(), "linguaflow_jobs_total ") {
		t.Fatal(got.Code, got.Body.String())
	}
	for _, query := range []string{"project_id=1", "access_token=" + token, "unexpected=1"} {
		if got := jobQueryRequest(router, "/api/v1/admin/runtime/summary?"+query, token); got.Code != 400 {
			t.Fatal(query, got.Code)
		}
	}
	c.User.UpdateOne(u).SetRole(service.SystemRoleUser).ExecX(ctx)
	for _, path := range []string{"/api/v1/admin/runtime/summary", "/metrics"} {
		if got := jobQueryRequest(router, path, token); got.Code != 403 {
			t.Fatal("revoked", path, got.Code)
		}
	}
	// Local mode still uses the current database role rather than the cached bootstrap pointer.
	u.Role = service.SystemRoleAdmin
	s.mode = config.ModeLocal
	s.localUser = u
	if got := jobQueryRequest(router, "/api/v1/admin/runtime/summary", ""); got.Code != 403 {
		t.Fatal("cached local admin", got.Code)
	}
	c.User.UpdateOne(u).SetRole(service.SystemRoleAdmin).ExecX(ctx)
	if got := jobQueryRequest(router, "/api/v1/admin/runtime/summary", ""); got.Code != 200 {
		t.Fatal("local admin", got.Code)
	}
	s.collector = nil
	if got := jobQueryRequest(router, "/api/v1/admin/runtime/summary", ""); got.Code != 503 {
		t.Fatal("missing collector", got.Code)
	}
}

func TestRuntimeSamplingNeedsNoBusinessDatabase(t *testing.T) {
	// Nil entClient is deliberate: collection must not query any business entity.
	a, b := &Server{collector: telemetry.NewCollector()}, &Server{collector: telemetry.NewCollector()}
	a.limiterPool = backend.NewLimiterPool()
	a.limiterPool.Initialize(map[int]int{9: 3})
	a.dispatcher = worker.NewDispatcher(nil, nil, config.WorkerConfig{})
	sample := func(s *Server) RuntimeSummary {
		w := httptest.NewRecorder()
		s.handleRuntimeSummary(w, httptest.NewRequest("GET", "/", nil))
		assertP4ResponseSchema(t, "/admin/runtime/summary", w)
		return jobQueryDecode[RuntimeSummary](t, w)
	}
	first, second := sample(a), sample(b)
	if first.InstanceId == second.InstanceId || first.UptimeSeconds < 0 {
		t.Fatal("instance isolation")
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"backend_id", "project_id", "api_key", "task_id"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal(string(raw))
		}
	}
}

type runtimeSignalListener struct {
	net.Listener
	accepted chan struct{}
	once     sync.Once
}

func (l *runtimeSignalListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.accepted) })
	return l.Listener.Accept()
}

func TestServerRuntimeLifecycle(t *testing.T) {
	for _, scenario := range []string{"shutdown", "parent_cancel", "serve_failure", "shutdown_before_run"} {
		t.Run(scenario, func(t *testing.T) {
			c := newTestEntClient(t)
			cfg := config.DefaultServerConfig()
			cfg.DataDir = t.TempDir()
			cfg.ShutdownTimeout = 2 * time.Second
			cfg.JWTSecret = strings.Repeat("runtime-test-key", 3)
			cfg.Credentials.KeyringFile = filepath.Join(cfg.DataDir, "keyring.json")
			if _, err := credential.PrepareKeyring(cfg.Credentials.KeyringFile, true); err != nil {
				t.Fatal(err)
			}
			if _, err := service.NewInitializationService(c).Initialize(context.Background(), config.ModeServer, config.BootstrapInput{Admin: &config.BootstrapAdmin{Username: "runtime-admin", Email: "runtime@test.invalid", Password: "runtime-password"}}); err != nil {
				t.Fatal(err)
			}
			s, err := NewServer(cfg, nil, nil, c, config.ModeServer, nil)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ln := &runtimeSignalListener{Listener: listener, accepted: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			if scenario == "shutdown_before_run" {
				if err := s.Shutdown(timeout); err != nil {
					t.Fatal(err)
				}
				if err := s.Run(ctx, ln); err == nil {
					t.Fatal("run after shutdown succeeded")
				}
				return
			}
			if scenario == "serve_failure" {
				listener.Close()
			}
			finished := make(chan error, 1)
			go func() { finished <- s.Run(ctx, ln) }()
			select {
			case <-ln.accepted:
			case <-timeout.Done():
				t.Fatal("server did not start")
			}
			if scenario == "shutdown" {
				if err := s.Shutdown(timeout); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "parent_cancel" {
				cancel()
			}
			select {
			case err := <-finished:
				if scenario == "serve_failure" {
					if err == nil {
						t.Fatal("missing serve failure")
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-timeout.Done():
				t.Fatal("server did not stop")
			}
			if s.ready.Load() {
				t.Fatal("stopped server remains ready")
			}
			if _, err := s.limiterPool.Lookup(1); !errors.Is(err, backend.ErrLimiterClosed) {
				t.Fatal("limiter not shut down", err)
			}
			for _, runner := range s.dispatcher.Snapshot() {
				if runner.State != "stopped" {
					t.Fatalf("runner not joined: %+v", runner)
				}
			}
		})
	}
}

func TestServerShutdownWaitsForExternalRequests(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	clients := telemetry.NewHTTPClients(telemetry.NewCollector(), runtimeBlockedTransport{entered: entered, release: release})
	s := &Server{httpClients: clients}
	request, _ := http.NewRequest("GET", "http://unused.invalid", nil)
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = clients.Client("openai", "generate").Do(request) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("uncooperative transport must report timeout", err)
	}
	close(release)
	<-finished
}

func TestManualServerShutdownUsesConfiguredBudget(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	clients := telemetry.NewHTTPClients(telemetry.NewCollector(), runtimeBlockedTransport{entered: entered, release: release})
	s := &Server{
		httpClients: clients,
		serverCfg:   &config.ServerConfig{ShutdownTimeout: 20 * time.Millisecond},
	}
	request, _ := http.NewRequest("GET", "http://unused.invalid", nil)
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = clients.Client("openai", "generate").Do(request) }()
	<-entered
	defer func() { close(release); <-finished }()
	shutdown := make(chan error, 1)
	go func() { shutdown <- s.Shutdown(context.Background()) }()
	select {
	case err := <-shutdown:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("configured shutdown budget must report timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual shutdown ignored configured budget")
	}
	if err := s.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("repeated shutdown lost original result", err)
	}
}

type runtimeBlockedTransport struct{ entered, release chan struct{} }

func (t runtimeBlockedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	close(t.entered)
	<-t.release
	return nil, context.Canceled
}
