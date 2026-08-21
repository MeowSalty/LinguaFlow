package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// jobEventsTestServer 构造仅含查询历史事件所需服务的 Server。
func jobEventsTestServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
	t.Helper()
	client := newTestEntClient(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := service.NewUserService(client, nil)
	projects := service.NewProjectService(client, users)
	jobSvc := service.NewJobService(client, projects, nil, nil, nil, nil, nil, nil, nil)
	s := &Server{
		serverCfg:   &config.ServerConfig{ServiceName: "test"},
		logger:      logger,
		entClient:   client,
		projectSvc:  projects,
		jobSvc:      jobSvc,
		eventBroker: event.NewBroker(nil).WithHistorian(event.NewEntEventStore(client)),
	}
	u, err := client.User.Create().
		SetUsername("testuser").
		SetPasswordHash("$2a$10$dummy").
		SetEmail("test@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return s, client, u
}

func newTestEntClient(t *testing.T) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(driver))
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = db.Close()
	})
	return client
}

func seedJobEvents(t *testing.T, client *ent.Client, u *ent.User) (*ent.Job, []*ent.SSEEvent) {
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
	var events []*ent.SSEEvent
	for i := int64(1); i <= 5; i++ {
		ev, err := client.SSEEvent.Create().
			SetJobID(job.ID).
			SetSeq(i).
			SetType("translation").
			SetLevel("info").
			SetStage("translate").
			SetMessage("event " + strconv.FormatInt(i, 10)).
			Save(context.Background())
		if err != nil {
			t.Fatalf("create sse event: %v", err)
		}
		events = append(events, ev)
	}
	return job, events
}

func jobEventsRequest(s *Server, jobID, afterSeq, limit int, u *ent.User) *httptest.ResponseRecorder {
	return jobEventsRequest2(s, jobID, afterSeq, limit, u, nil)
}

func jobEventsRequest2(s *Server, jobID, afterSeq, limit int, u *ent.User, beforeSeq *int) *httptest.ResponseRecorder {
	path := "/jobs/" + strconv.Itoa(jobID) + "/events"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if limit > 0 {
		q := req.URL.Query()
		q.Set("limit", strconv.Itoa(limit))
		req.URL.RawQuery = q.Encode()
	}
	if afterSeq >= 0 {
		q := req.URL.Query()
		q.Set("after_seq", strconv.Itoa(afterSeq))
		req.URL.RawQuery = q.Encode()
	}
	if beforeSeq != nil {
		q := req.URL.Query()
		q.Set("before_seq", strconv.Itoa(*beforeSeq))
		req.URL.RawQuery = q.Encode()
	}
	if u != nil {
		req = withAuthUser(req, u)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("jobId", strconv.Itoa(jobID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleListJobEvents(w, req)
	return w
}

func TestHandler_ListJobEvents_Pagination(t *testing.T) {
	s, client, u := jobEventsTestServer(t)
	job, _ := seedJobEvents(t, client, u)

	// 第一页 limit=2
	w := jobEventsRequest(s, job.ID, 0, 2, u)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var resp JobEventListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 || resp.Items[0].Seq != 1 || resp.Items[1].Seq != 2 {
		t.Fatalf("want 2 items seq [1,2], got %+v", resp.Items)
	}
	if resp.NextAfterSeq == nil || *resp.NextAfterSeq != 2 {
		t.Errorf("next_after_seq = %v, want 2", resp.NextAfterSeq)
	}
	if resp.Items[0].Stage == nil || *resp.Items[0].Stage != "translate" {
		t.Errorf("stage not mapped: %+v", resp.Items[0])
	}

	// 第二页 after_seq=2 limit=2
	w = jobEventsRequest(s, job.ID, 2, 2, u)
	resp = JobEventListResponse{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 || resp.Items[0].Seq != 3 || resp.Items[1].Seq != 4 {
		t.Fatalf("want seq [3,4], got %+v", resp.Items)
	}
	if resp.NextAfterSeq == nil || *resp.NextAfterSeq != 4 {
		t.Errorf("next_after_seq = %v, want 4", resp.NextAfterSeq)
	}

	// 最后一页 after_seq=4 limit=2
	w = jobEventsRequest(s, job.ID, 4, 2, u)
	resp = JobEventListResponse{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Seq != 5 {
		t.Fatalf("want 1 item seq [5], got %+v", resp.Items)
	}
	if resp.NextAfterSeq != nil {
		t.Errorf("next_after_seq = %v, want <nil> on last page", resp.NextAfterSeq)
	}
}

func TestHandler_ListJobEvents_Unauthorized(t *testing.T) {
	s, client, u := jobEventsTestServer(t)
	job, _ := seedJobEvents(t, client, u)
	w := jobEventsRequest(s, job.ID, 0, 0, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var problem struct {
		Type  string `json:"type"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Type != "urn:linguaflow:unauthorized" {
		t.Errorf("type = %q, want urn:linguaflow:unauthorized", problem.Type)
	}
	if problem.Title != "unauthorized" {
		t.Errorf("title = %q, want unauthorized", problem.Title)
	}
}

func TestHandler_ListJobEvents_NotFound(t *testing.T) {
	s, _, u := jobEventsTestServer(t)
	w := jobEventsRequest(s, 999999, 0, 0, u)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandler_ListJobEvents_Forbidden(t *testing.T) {
	s, client, other := jobEventsTestServer(t)

	owner, err := client.User.Create().
		SetUsername("owner").
		SetPasswordHash("$2a$10$dummy").
		SetEmail("owner@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	p, err := client.Project.Create().
		SetName("owner-proj").
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(owner.ID).
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
	w := jobEventsRequest(s, job.ID, 0, 0, other)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandler_ListJobEvents_Default_RecentPage(t *testing.T) {
	// 两参缺省：返回最近一页（终态任务首屏「最新优先」），而非最旧一页
	s, client, u := jobEventsTestServer(t)
	job, _ := seedJobEvents(t, client, u) // seq 1..5

	req := httptest.NewRequest(http.MethodGet, "/jobs/"+strconv.Itoa(job.ID)+"/events?limit=2", nil)
	req = withAuthUser(req, u)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("jobId", strconv.Itoa(job.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleListJobEvents(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var resp JobEventListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 最近 2 条应为 seq [4,5]（升序），而非最旧的 [1,2]
	if len(resp.Items) != 2 || resp.Items[0].Seq != 4 || resp.Items[1].Seq != 5 {
		t.Fatalf("want recent items seq [4,5], got %+v", resp.Items)
	}
	if resp.NextBeforeSeq == nil || *resp.NextBeforeSeq != 4 {
		t.Errorf("next_before_seq = %v, want 4 (older page cursor)", resp.NextBeforeSeq)
	}
}

func TestHandler_ListJobEvents_BackwardPagination(t *testing.T) {
	s, client, u := jobEventsTestServer(t)
	job, _ := seedJobEvents(t, client, u) // seq 1..5

	// 反向第一页：before_seq=5 → seq < 5 的最近 limit=2 条，升序返回 [3,4]
	before := 5
	w := jobEventsRequest2(s, job.ID, 0, 2, u, &before)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var resp JobEventListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 || resp.Items[0].Seq != 3 || resp.Items[1].Seq != 4 {
		t.Fatalf("want items seq [3,4], got %+v", resp.Items)
	}
	if resp.NextBeforeSeq == nil || *resp.NextBeforeSeq != 3 {
		t.Errorf("next_before_seq = %v, want 3", resp.NextBeforeSeq)
	}

	// 反向第二页：before_seq=3 → seq < 3 的最近 2 条 [1,2]
	before = 3
	w = jobEventsRequest2(s, job.ID, 0, 2, u, &before)
	resp = JobEventListResponse{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 || resp.Items[0].Seq != 1 || resp.Items[1].Seq != 2 {
		t.Fatalf("want items seq [1,2], got %+v", resp.Items)
	}
	if resp.NextBeforeSeq != nil {
		t.Errorf("next_before_seq = %v, want <nil> at oldest boundary", resp.NextBeforeSeq)
	}
}

func TestHandler_ListJobEvents_BeforeSeq_OverlapLeak(t *testing.T) {
	// 反向分页不得泄漏边界：before_seq=3 只返回 seq<3，绝不能返回 3/4/5
	s, client, u := jobEventsTestServer(t)
	job, _ := seedJobEvents(t, client, u)
	before := 3
	w := jobEventsRequest2(s, job.ID, 0, 100, u, &before)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp JobEventListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 || resp.Items[0].Seq != 1 || resp.Items[1].Seq != 2 {
		t.Fatalf("want exactly seq [1,2] (no boundary leak), got %+v", resp.Items)
	}
}

func TestHandler_ListJobEvents_InvalidBeforeSeq(t *testing.T) {
	s, client, u := jobEventsTestServer(t)
	job, _ := seedJobEvents(t, client, u)
	invalid := 0
	w := jobEventsRequest2(s, job.ID, 0, 0, u, &invalid)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for before_seq=0", w.Code)
	}
}
