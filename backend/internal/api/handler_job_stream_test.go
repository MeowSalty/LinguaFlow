package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// jobStreamTestServer 构造含 HybridStore（ring + DB 回退）的 Server，
// 用于验证 SSE 新连接最近窗口回放。
func jobStreamTestServer(t *testing.T, ringCapacity, replayBatch, maxReplay int) (*Server, *ent.Client, *ent.User) {
	t.Helper()
	client := newTestEntClient(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := service.NewUserService(client, nil)
	projects := service.NewProjectService(client, users)
	jobSvc := service.NewJobService(client, projects, nil, nil, nil, nil, nil, nil, nil)
	entStore := event.NewEntEventStore(client)
	hybridStore, err := event.NewHybridStore(
		event.NewRingBufferStore(event.RingBufferConfig{Capacity: ringCapacity}),
		entStore,
	)
	if err != nil {
		t.Fatalf("new hybrid store: %v", err)
	}
	s := &Server{
		serverCfg:      &config.ServerConfig{ServiceName: "test"},
		logger:         logger,
		entClient:      client,
		projectSvc:     projects,
		jobSvc:         jobSvc,
		eventBroker:    event.NewBroker(hybridStore).WithHistorian(entStore),
		sseReplayBatch: replayBatch,
		sseMaxReplay:   maxReplay,
	}
	u, err := client.User.Create().
		SetUsername("streamuser").
		SetPasswordHash("$2a$10$dummy").
		SetEmail("stream@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return s, client, u
}

// seedJobEventsN 直接向 DB 写入 job 的 n 条事件（seq 1..n），模拟
// 服务重启后 ring 为空、历史全在 DB 的场景。
func seedJobEventsN(t *testing.T, client *ent.Client, u *ent.User, n int) (*ent.Job, error) {
	t.Helper()
	p, err := client.Project.Create().
		SetName("test-proj").
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(u.ID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	job, err := client.Job.Create().
		SetProjectID(p.ID).
		SetExecutionPlanID(1).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	for i := int64(1); i <= int64(n); i++ {
		if _, err := client.SSEEvent.Create().
			SetJobID(job.ID).
			SetSeq(i).
			SetType("translation").
			SetLevel("info").
			SetStage("translate").
			SetMessage("event " + strconv.FormatInt(i, 10)).
			Save(context.Background()); err != nil {
			return nil, err
		}
	}
	return job, nil
}

// safeRecorder 包装 httptest.ResponseRecorder，对其底层 *bytes.Buffer
// 的读取（Body.String）与并发写入加互斥锁，避免竞态检测报错。
type safeRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newSafeRecorder() *safeRecorder {
	return &safeRecorder{ResponseRecorder: httptest.NewRecorder()}
}

// Write 在 handler goroutine 写入 SSE 数据时加锁，保护共享 buffer。
func (s *safeRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Write(b)
}

// String 安全读取当前已写入的响应体，供测试主 goroutine 轮询。
func (s *safeRecorder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Body.String()
}

// streamSSE 调用 handleJobStream，收集回放段写入的 SSE 事件后取消连接。
// expected 为期待的回放事件数，用于轮询等待回放完成。
func streamSSE(t *testing.T, s *Server, jobID int, u *ent.User, lastEventID string, expected int) []int64 {
	t.Helper()
	path := "/jobs/" + strconv.Itoa(jobID) + "/stream"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	if u != nil {
		req = withAuthUser(req, u)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("jobId", strconv.Itoa(jobID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := newSafeRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleJobStream(w, req)
	}()

	// 等待回放写入足够事件后取消，避免阻塞在实时循环
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countIDLines(w.String()) >= expected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	seqs := parseIDLines(w.String())
	if expected >= 0 && len(seqs) != expected {
		t.Fatalf("expected %d replayed events, got %d (%v); body=%q", expected, len(seqs), seqs, w.String())
	}
	return seqs
}

var sseIDLineRE = regexp.MustCompile(`(?m)^id: (\d+)$`)

func countIDLines(body string) int {
	return len(sseIDLineRE.FindAllStringSubmatch(body, -1))
}

func parseIDLines(body string) []int64 {
	var seqs []int64
	for _, m := range sseIDLineRE.FindAllStringSubmatch(body, -1) {
		v, _ := strconv.ParseInt(m[1], 10, 64)
		seqs = append(seqs, v)
	}
	return seqs
}

func TestHandler_JobStream_FreshConnection_ReplaysRecentWindow(t *testing.T) {
	const (
		total     = 600
		maxReplay = 100
	)
	s, client, u := jobStreamTestServer(t, 32, 50, maxReplay)
	job, err := seedJobEventsN(t, client, u, total)
	if err != nil {
		t.Fatalf("seed events: %v", err)
	}

	// 新连接无 Last-Event-ID：回放最近窗口 maxReplay 条，而不是 seq 1 起的最旧事件
	seqs := streamSSE(t, s, job.ID, u, "", maxReplay)
	wantFirst := int64(total - maxReplay + 1)
	if seqs[0] != wantFirst {
		t.Fatalf("expected first replayed seq=%d (recent window), got %d", wantFirst, seqs[0])
	}
	if seqs[len(seqs)-1] != int64(total) {
		t.Fatalf("expected last replayed seq=%d, got %d", total, seqs[len(seqs)-1])
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("expected ascending consecutive seqs, got %v", seqs)
		}
	}
}

func TestHandler_JobStream_Reconnect_ResumesFromCursor(t *testing.T) {
	const total = 600
	s, client, u := jobStreamTestServer(t, 32, 50, 100)
	job, err := seedJobEventsN(t, client, u, total)
	if err != nil {
		t.Fatalf("seed events: %v", err)
	}

	// 重连带 Last-Event-ID=595：只应收到 seq 596..600
	seqs := streamSSE(t, s, job.ID, u, "595", 5)
	if seqs[0] != 596 || seqs[4] != 600 {
		t.Fatalf("expected seqs [596..600], got %v", seqs)
	}
}

func TestHandler_JobStream_FreshConnection_WhenFewerThanWindow(t *testing.T) {
	s, client, u := jobStreamTestServer(t, 32, 50, 100)
	job, err := seedJobEventsN(t, client, u, 3)
	if err != nil {
		t.Fatalf("seed events: %v", err)
	}

	// 事件数少于窗口：全部回放（seq 1..3）
	seqs := streamSSE(t, s, job.ID, u, "", 3)
	if seqs[0] != 1 || seqs[2] != 3 {
		t.Fatalf("expected seqs [1,2,3], got %v", seqs)
	}
}
