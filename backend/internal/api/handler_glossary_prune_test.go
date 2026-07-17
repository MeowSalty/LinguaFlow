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
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// newTestServer 创建带内存 SQLite 的 Server（仅含必要服务）。
func newTestServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
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

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := service.NewUserService(client, service.NewAuthService(client, service.AuthConfig{}, service.NewAdminService(client)))
	projects := service.NewProjectService(client, users)
	glossarySvc := service.NewGlossaryService(client, projects)
	prunePromptTemplateSvc := service.NewPrunePromptTemplateService(client)
	glossaryPruneSvc := service.NewGlossaryPruneService(client, projects, service.NewBackendService(client, users, nil), glossarySvc, prunePromptTemplateSvc, nil, logger)

	// 创建测试用户
	u, err := client.User.Create().
		SetUsername("testuser").
		SetPasswordHash("$2a$10$dummy").
		SetEmail("test@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	s := &Server{
		serverCfg:              &config.ServerConfig{ServiceName: "test"},
		logger:                 logger,
		entClient:              client,
		glossarySvc:            glossarySvc,
		prunePromptTemplateSvc: prunePromptTemplateSvc,
		glossaryPruneSvc:       glossaryPruneSvc,
	}
	return s, client, u
}

// withAuthUser 将认证用户注入请求 context。
func withAuthUser(r *http.Request, u *ent.User) *http.Request {
	ctx := context.WithValue(r.Context(), authContextKey{}, authenticatedUser{User: u})
	return r.WithContext(ctx)
}

func TestHandler_ListPrunePromptTemplates_IncludesBuiltin(t *testing.T) {
	s, _, u := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/prune-prompt-templates", nil)
	req = withAuthUser(req, u)
	w := httptest.NewRecorder()
	s.handleListPrunePromptTemplates(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp PrunePromptTemplateListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Id != -1 {
		t.Errorf("want 1 builtin template with id -1, got %+v", resp.Items)
	}
}

func TestHandler_GetPrunePromptTemplate_Builtin(t *testing.T) {
	s, _, u := newTestServer(t)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("prunePromptTemplateId", "-1")
	req := httptest.NewRequest(http.MethodGet, "/prune-prompt-templates/-1", nil)
	req = req.WithContext(context.WithValue(context.WithValue(req.Context(), chi.RouteCtxKey, rctx), authContextKey{}, authenticatedUser{User: u}))

	w := httptest.NewRecorder()
	s.handleGetPrunePromptTemplate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp PrunePromptTemplate
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Id != -1 || resp.Name == "" {
		t.Errorf("unexpected template: %+v", resp)
	}
}

func TestHandler_PreviewGlossaryPrune_EmptyGlossary(t *testing.T) {
	s, client, u := newTestServer(t)

	// 创建项目
	p, err := client.Project.Create().
		SetName("test-proj").
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(u.ID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	body, _ := json.Marshal(GlossaryPruneRequest{BackendId: 1})
	req := httptest.NewRequest(http.MethodPost, "/projects/"+strconv.Itoa(p.ID)+"/glossary/prune", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAuthUser(req, u)

	w := httptest.NewRecorder()
	s.handlePreviewGlossaryPrune(w, req, p.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var resp GlossaryPrunePreview
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 0 || len(resp.Suggestions) != 0 {
		t.Errorf("want empty preview, got %+v", resp)
	}
}

func TestHandler_PreviewGlossaryPrune_Forbidden(t *testing.T) {
	s, client, _ := newTestServer(t)

	// 创建项目和另一个用户
	owner, _ := client.User.Create().
		SetUsername("owner").
		SetPasswordHash("$2a$10$dummy").
		SetEmail("owner@test.com").
		Save(context.Background())
	other, _ := client.User.Create().
		SetUsername("other").
		SetPasswordHash("$2a$10$dummy").
		SetEmail("other@test.com").
		Save(context.Background())
	p, _ := client.Project.Create().
		SetName("owner-proj").
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(owner.ID).
		Save(context.Background())

	body, _ := json.Marshal(GlossaryPruneRequest{BackendId: 1})
	req := httptest.NewRequest(http.MethodPost, "/projects/"+strconv.Itoa(p.ID)+"/glossary/prune", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAuthUser(req, other)

	w := httptest.NewRecorder()
	s.handlePreviewGlossaryPrune(w, req, p.ID)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandler_PreviewGlossaryPrune_ProjectNotFound(t *testing.T) {
	s, _, u := newTestServer(t)

	body, _ := json.Marshal(GlossaryPruneRequest{BackendId: 1})
	req := httptest.NewRequest(http.MethodPost, "/projects/999999/glossary/prune", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAuthUser(req, u)

	w := httptest.NewRecorder()
	s.handlePreviewGlossaryPrune(w, req, 999999)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandler_ApplyGlossaryPrune_DeleteAndUpdate(t *testing.T) {
	s, client, u := newTestServer(t)

	p, _ := client.Project.Create().
		SetName("apply-proj").
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(u.ID).
		Save(context.Background())

	e1, _ := client.GlossaryEntry.Create().SetProjectID(p.ID).SetSourceKey("del").SetSource("DeleteMe").SetTarget("删除我").SetNotes("").Save(context.Background())
	e2, _ := client.GlossaryEntry.Create().SetProjectID(p.ID).SetSourceKey("upd").SetSource("UpdateMe").SetTarget("旧").SetNotes("").Save(context.Background())

	target := "新译"
	notes := "新注"
	body, _ := json.Marshal(GlossaryPruneApplyRequest{
		Changes: []struct {
			Action  GlossaryPruneApplyRequestChangesAction `json:"action"`
			EntryId int                                    `json:"entry_id"`
			Notes   *string                                `json:"notes,omitempty"`
			Target  *string                                `json:"target,omitempty"`
		}{
			{Action: GlossaryPruneApplyRequestChangesActionDelete, EntryId: e1.ID},
			{Action: GlossaryPruneApplyRequestChangesActionUpdate, EntryId: e2.ID, Target: &target, Notes: &notes},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/projects/"+strconv.Itoa(p.ID)+"/glossary/prune/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAuthUser(req, u)

	w := httptest.NewRecorder()
	s.handleApplyGlossaryPrune(w, req, p.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var resp GlossaryPruneApplyResult
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Deleted != 1 || resp.Updated != 1 || resp.Failed != 0 {
		t.Errorf("result mismatch: %+v", resp)
	}
}

func TestHandler_ApplyGlossaryPrune_PartialFailure(t *testing.T) {
	s, client, u := newTestServer(t)

	p, _ := client.Project.Create().
		SetName("partial-proj").
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(u.ID).
		Save(context.Background())

	e1, _ := client.GlossaryEntry.Create().SetProjectID(p.ID).SetSourceKey("real").SetSource("Real").SetTarget("真").SetNotes("").Save(context.Background())

	body, _ := json.Marshal(GlossaryPruneApplyRequest{
		Changes: []struct {
			Action  GlossaryPruneApplyRequestChangesAction `json:"action"`
			EntryId int                                    `json:"entry_id"`
			Notes   *string                                `json:"notes,omitempty"`
			Target  *string                                `json:"target,omitempty"`
		}{
			{Action: GlossaryPruneApplyRequestChangesActionDelete, EntryId: 999999}, // failed
			{Action: GlossaryPruneApplyRequestChangesActionDelete, EntryId: e1.ID},  // success
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/projects/"+strconv.Itoa(p.ID)+"/glossary/prune/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAuthUser(req, u)

	w := httptest.NewRecorder()
	s.handleApplyGlossaryPrune(w, req, p.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp GlossaryPruneApplyResult
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Deleted != 1 || resp.Failed != 1 {
		t.Errorf("result mismatch: %+v", resp)
	}
}
