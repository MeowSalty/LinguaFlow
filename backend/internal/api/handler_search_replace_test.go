package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// srTestServer 构造带内存 SQLite 的 Server，注入 segmentSvc 与 auditSvc。
// 用独立 db 并设 MaxOpenConns(1)，确保事务与普通查询共享同一连接（:memory: 连接私有）。
func srTestServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(driver))
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := service.NewUserService(client, service.NewAuthService(client, service.AuthConfig{}, service.NewAdminService(client)))
	projects := service.NewProjectService(client, users)
	segmentSvc := service.NewSegmentService(client, projects, dialect.SQLite, 90*24*time.Hour, logger)
	auditSvc := service.NewAuditService(client, users, projects)

	u, err := client.User.Create().
		SetUsername("sr-handler-user").
		SetPasswordHash("$2a$10$dummy").
		SetEmail("sr@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	s := &Server{
		serverCfg:  newSRServerCfg(),
		logger:     logger,
		entClient:  client,
		segmentSvc: segmentSvc,
		auditSvc:   auditSvc,
	}
	return s, client, u
}

// srSeedResource 创建项目+资源，并按 targets 建 translated 段落，返回 project/res id。
func srSeedResource(t *testing.T, client *ent.Client, userID int, targets ...string) (int, int) {
	t.Helper()
	ctx := context.Background()
	project, err := client.Project.Create().SetName("sr-proj").SetOwnerUserID(userID).Save(ctx)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	res, err := client.Resource.Create().
		SetProjectID(project.ID).SetPath("chapters/sr.txt").SetFormat("txt").SetStoragePath("storage/sr.txt").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resource: %v", err)
	}
	for i, tgt := range targets {
		if _, err := client.Segment.Create().
			SetResourceID(res.ID).SetSegmentIndex(i).SetSourceText("source").
			SetTargetText(tgt).SetStatus(segment.StatusTranslated).Save(ctx); err != nil {
			t.Fatalf("create segment %d: %v", i, err)
		}
	}
	return project.ID, res.ID
}

// srRequest 构造带 chi path params 的认证请求并直接调用给定 handler（不经 router），
// 避免新路由器要求 Server 装配全量服务。
func srRequest(s *Server, method string, body any, u *ent.User, handler http.HandlerFunc, pathParams map[string]string) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/", rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rctx := chi.NewRouteContext()
	for k, v := range pathParams {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = withAuthUser(req, u)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHandler_SearchReplacePreviewHappyPath(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "colour pen", "plain text", "colour colour")

	rec := srRequest(s, http.MethodPost,
		map[string]any{"find": "colour", "replace_with": "color"}, u,
		s.handlePreviewResourceSegmentsSearchReplace,
		map[string]string{"projectId": strconv.Itoa(projectID), "resourceId": strconv.Itoa(resID)})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status=%d want 200, body=%s", rec.Code, rec.Body.String())
	}
	var resp searchReplacePreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.MatchedSegmentCount != 2 {
		t.Fatalf("matched_segment_count=%d want 2", resp.MatchedSegmentCount)
	}
	if resp.TotalReplacements != 3 {
		t.Fatalf("total_replacements=%d want 3", resp.TotalReplacements)
	}
}

func TestHandler_SearchReplaceApplyUndoFlow(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "colour pen", "colour colour")

	rec := srRequest(s, http.MethodPost,
		map[string]any{"find": "colour", "replace_with": "color"}, u,
		s.handleApplyResourceSegmentsSearchReplace,
		map[string]string{"projectId": strconv.Itoa(projectID), "resourceId": strconv.Itoa(resID)})
	if rec.Code != http.StatusOK {
		t.Fatalf("apply status=%d want 200, body=%s", rec.Code, rec.Body.String())
	}
	var applyResp searchReplaceApplyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &applyResp); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if applyResp.AppliedCount != 2 {
		t.Fatalf("applied_count=%d want 2", applyResp.AppliedCount)
	}
	if applyResp.OperationID == "" {
		t.Fatalf("operation_id empty")
	}

	rec = srRequest(s, http.MethodPost, nil, u,
		s.handleUndoResourceSegmentsSearchReplace,
		map[string]string{"projectId": strconv.Itoa(projectID), "resourceId": strconv.Itoa(resID), "operationId": applyResp.OperationID})
	if rec.Code != http.StatusOK {
		t.Fatalf("undo status=%d want 200, body=%s", rec.Code, rec.Body.String())
	}
	var undoResp searchReplaceUndoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &undoResp); err != nil {
		t.Fatalf("decode undo: %v", err)
	}
	if undoResp.UndoneCount != 2 {
		t.Fatalf("undone_count=%d want 2", undoResp.UndoneCount)
	}
}

func TestHandler_SearchReplacePreviewInvalidInput400(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "x")

	rec := srRequest(s, http.MethodPost,
		map[string]any{"find": "", "replace_with": "y"}, u,
		s.handlePreviewResourceSegmentsSearchReplace,
		map[string]string{"projectId": strconv.Itoa(projectID), "resourceId": strconv.Itoa(resID)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content-type=%q want application/problem+json", ct)
	}
}

func TestHandler_SearchReplacePreviewInvalidRegex400(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "x")

	rec := srRequest(s, http.MethodPost,
		map[string]any{"find": "[", "replace_with": "y", "match_mode": "regex"}, u,
		s.handlePreviewResourceSegmentsSearchReplace,
		map[string]string{"projectId": strconv.Itoa(projectID), "resourceId": strconv.Itoa(resID)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_SearchReplaceUndoNotFound404(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "x")

	rec := srRequest(s, http.MethodPost, nil, u,
		s.handleUndoResourceSegmentsSearchReplace,
		map[string]string{"projectId": strconv.Itoa(projectID), "resourceId": strconv.Itoa(resID), "operationId": "nonexistent-op"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_SearchReplaceResourceNotFound404(t *testing.T) {
	s, _, u := srTestServer(t)
	rec := srRequest(s, http.MethodPost,
		map[string]any{"find": "x", "replace_with": "y"}, u,
		s.handlePreviewResourceSegmentsSearchReplace,
		map[string]string{"projectId": "1", "resourceId": "999999"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404, body=%s", rec.Code, rec.Body.String())
	}
}

// newSRServerCfg 构造最小 ServerConfig 仅满足 handler 路径校验。
func newSRServerCfg() *config.ServerConfig {
	cfg := config.DefaultServerConfig()
	return cfg
}
