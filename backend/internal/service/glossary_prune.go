package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrPruneLLMCallFailed = errors.New("prune: llm call failed")
	ErrPruneParseFailed   = errors.New("prune: parse response failed")
)

// PruneDiagnostics 携带 Preview 调用过程中的诊断信息（与 SSE batch 事件对齐）。
type PruneDiagnostics struct {
	BackendName      string   `json:"backend_name,omitempty"`
	TemplateName     string   `json:"template_name,omitempty"`
	DurationMs       int      `json:"duration_ms,omitempty"`
	PromptTokens     int      `json:"prompt_tokens,omitempty"`
	CompletionTokens int      `json:"completion_tokens,omitempty"`
	SystemPrompt     string   `json:"system_prompt,omitempty"`
	UserMessage      string   `json:"user_message,omitempty"`
	SystemTruncated  bool     `json:"system_truncated,omitempty"`
	UserTruncated    bool     `json:"user_truncated,omitempty"`
	SystemLength     int      `json:"system_length,omitempty"`
	UserLength       int      `json:"user_length,omitempty"`
	ReceivedContent  string   `json:"received_content,omitempty"`
	RecvTruncated    bool     `json:"received_truncated,omitempty"`
	RecvLength       int      `json:"received_length,omitempty"`
	EntryCount       int      `json:"entry_count,omitempty"`
	ParsedCount      int      `json:"parsed_count,omitempty"`
	RepairedOps      []string `json:"repaired_ops,omitempty"`
	ErrorType        string   `json:"error_type,omitempty"`
	ErrorMessage     string   `json:"error_message,omitempty"`
	HTTPStatus       int      `json:"http_status,omitempty"`
}

// PruneError 包装 sentinel 错误并携带诊断信息，使 handler 可在错误响应中返回收发内容。
type PruneError struct {
	Sentinel    error
	Diagnostics PruneDiagnostics
}

func (e *PruneError) Error() string {
	if e.Sentinel != nil {
		return e.Sentinel.Error()
	}
	return "prune error"
}

func (e *PruneError) Unwrap() error { return e.Sentinel }

// PruneSuggestion 是 Preview 返回的单条精简建议。
type PruneSuggestion struct {
	EntryID       int    `json:"entry_id"`
	Action        string `json:"action"` // "delete" | "update"
	Source        string `json:"source"`
	OldTarget     string `json:"old_target"`
	NewTarget     string `json:"new_target"`
	OldNotes      string `json:"old_notes"`
	NewNotes      string `json:"new_notes"`
	TargetChanged bool   `json:"target_changed"`
	NotesChanged  bool   `json:"notes_changed"`
}

// PrunePreview 是 Preview 的返回结构。
type PrunePreview struct {
	Suggestions []PruneSuggestion `json:"suggestions"`
	Total       int               `json:"total"`
	ToDelete    int               `json:"to_delete"`
	ToUpdate    int               `json:"to_update"`
	ToKeep      int               `json:"to_keep"`
	Diagnostics PruneDiagnostics  `json:"diagnostics"`
}

// PruneChange 是 Apply 请求中的单条变更。
type PruneChange struct {
	EntryID int    `json:"entry_id"`
	Action  string `json:"action"` // "delete" | "update"
	Target  string `json:"target,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

// PruneApplyResult 是 Apply 的返回结构。
type PruneApplyResult struct {
	Deleted int `json:"deleted"`
	Updated int `json:"updated"`
	Failed  int `json:"failed"`
}

// GlossaryPruneService 提供术语表精简的 Preview 与 Apply 操作。
type GlossaryPruneService struct {
	client         *ent.Client
	projects       *ProjectService
	backends       *BackendService
	glossary       *GlossaryService
	pruneTemplates *PrunePromptTemplateService
	limiterPool    *backend.LimiterPool
	logger         *slog.Logger
}

// NewGlossaryPruneService 创建 GlossaryPruneService 实例。
func NewGlossaryPruneService(
	client *ent.Client,
	projects *ProjectService,
	backends *BackendService,
	glossary *GlossaryService,
	pruneTemplates *PrunePromptTemplateService,
	limiterPool *backend.LimiterPool,
	logger *slog.Logger,
) *GlossaryPruneService {
	if logger == nil {
		logger = slog.Default()
	}
	return &GlossaryPruneService{
		client:         client,
		projects:       projects,
		backends:       backends,
		glossary:       glossary,
		pruneTemplates: pruneTemplates,
		limiterPool:    limiterPool,
		logger:         logger,
	}
}

// defaultPruneRepairOptions 返回精简场景的默认修复选项。
func defaultPruneRepairOptions() repair.Options {
	return repair.Options{
		JSONStructural: true,
		SchemaAliases:  true,
	}
}

// defaultPruneRetryPolicy 返回精简场景的默认重试策略。
func defaultPruneRetryPolicy() backend.RetryPolicy {
	return backend.RetryPolicy{
		MaxAttempts: 3,
		Backoff:     2000,
		Jitter:      true,
	}
}

// validateBackendForProject 检查后端对项目是否可访问（参照 JobService.validateBackendAccess）。
func (s *GlossaryPruneService) validateBackendForProject(ctx context.Context, projectRow *ent.Project, b *BackendRecord) error {
	if projectRow.OwnerUserID != nil {
		if b.Scope == ScopeUser && b.OwnerUserID != nil && *b.OwnerUserID == *projectRow.OwnerUserID {
			return nil
		}
		if b.Scope == ScopeOrg && b.OwnerOrgID != nil && s.userBelongsToOrg(ctx, *projectRow.OwnerUserID, *b.OwnerOrgID) {
			return nil
		}
		return ErrForbidden
	}
	if projectRow.OwnerOrgID != nil {
		if b.Scope == ScopeOrg && b.OwnerOrgID != nil && *b.OwnerOrgID == *projectRow.OwnerOrgID {
			return nil
		}
		return ErrForbidden
	}
	return ErrProjectOwnerConflict
}

// userBelongsToOrg 检查用户是否属于指定组织。
func (s *GlossaryPruneService) userBelongsToOrg(ctx context.Context, userID, orgID int) bool {
	count, err := s.client.OrgMembership.Query().
		Where(
			orgmembership.HasOrganizationWith(organization.IDEQ(orgID)),
			orgmembership.HasUserWith(user.IDEQ(userID)),
		).
		Count(ctx)
	return err == nil && count > 0
}

// Preview 调用 LLM 分析现有术语表，返回精简建议（不修改数据）。
func (s *GlossaryPruneService) Preview(ctx context.Context, actorUserID, projectID, backendID int, templateID int) (*PrunePreview, error) {
	projectRow, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}

	entries, err := s.client.GlossaryEntry.Query().
		Where(glossaryentry.ProjectIDEQ(projectID)).
		Order(ent.Asc(glossaryentry.FieldSourceKey), ent.Asc(glossaryentry.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("prune: load glossary entries: %w", err)
	}

	if len(entries) == 0 {
		return &PrunePreview{Suggestions: []PruneSuggestion{}}, nil
	}

	if templateID == 0 {
		templateID = templates.BuiltinPrunePromptTemplateID
	}
	tmpl, err := s.pruneTemplates.GetByID(ctx, templateID)
	if err != nil {
		return nil, err
	}

	renderer, err := prompt.NewPruneRenderer(tmpl.Content)
	if err != nil {
		return nil, fmt.Errorf("prune: create renderer: %w", err)
	}

	pruneEntries := make([]prompt.PruneEntry, 0, len(entries))
	for _, e := range entries {
		pruneEntries = append(pruneEntries, prompt.PruneEntry{
			Source: e.Source,
			Target: e.Target,
			Notes:  e.Notes,
		})
	}

	sys, usr, err := renderer.Render(prompt.PruneData{
		SourceLang: projectRow.SourceLang,
		TargetLang: projectRow.TargetLang,
		Entries:    pruneEntries,
	})
	if err != nil {
		return nil, fmt.Errorf("prune: render prompt: %w", err)
	}

	backendRecord, err := s.backends.GetByID(ctx, backendID)
	if err != nil {
		return nil, err
	}
	if err := s.validateBackendForProject(ctx, projectRow, backendRecord); err != nil {
		return nil, err
	}

	b, err := backend.Build(backend.Config{
		Name:    backendRecord.Name,
		Type:    backendRecord.Type,
		Enabled: true,
		Options: backendRecord.Options,
	})
	if err != nil {
		return nil, fmt.Errorf("prune: build backend: %w", err)
	}
	defer b.Close()

	if s.limiterPool != nil && backendRecord.RateLimitPerMinute > 0 {
		limiter := s.limiterPool.Get(backendRecord.ID, backendRecord.RateLimitPerMinute)
		b = backend.NewRateLimitedBackend(b, limiter)
	}

	start := time.Now()

	s.logger.Info("prune preview started",
		"project_id", projectID,
		"entry_count", len(entries),
		"backend_name", backendRecord.Name,
		"template_name", tmpl.Name)

	// system 和 user 分开输出用于诊断（与 SSE 一致：sent_content 仅含 user）
	req := backend.Request{
		System:         sys,
		User:           usr,
		ResponseFormat: "json_schema",
		JSONSchema:     prompt.BootstrapSchema(),
	}

	s.logger.Debug("prune prompt sent",
		"system_len", len(sys),
		"user_len", len(usr),
		"entry_count", len(entries))

	var resp *backend.Response
	callErr := backend.WithRetry(ctx, defaultPruneRetryPolicy(), func() error {
		var rerr error
		resp, rerr = b.Translate(ctx, req)
		return rerr
	})
	if callErr != nil {
		if errors.Is(callErr, context.Canceled) {
			s.logger.Warn("prune preview cancelled", "err", callErr)
		} else {
			s.logger.Warn("prune LLM call failed", "backend_name", backendRecord.Name, "err", callErr)
		}
		diag := s.buildDiagnostics(backendRecord, tmpl, start, sys, usr, "", 0, nil)
		diag.ErrorType = "backend_error"
		diag.ErrorMessage = callErr.Error()
		diag.HTTPStatus = extractHTTPStatus(callErr)
		return nil, &PruneError{Sentinel: fmt.Errorf("%w: %w", ErrPruneLLMCallFailed, callErr), Diagnostics: diag}
	}

	repairOpts := defaultPruneRepairOptions()
	refined, parseRepaired, perr := repair.TryRepairBootstrap(resp.Text, repairOpts)
	if perr != nil {
		s.logger.Warn("prune parse failed",
			"resp_len", len(resp.Text),
			"resp_head", headSnippetLocal(resp.Text, 200),
			"repaired", parseRepaired)
		diag := s.buildDiagnostics(backendRecord, tmpl, start, sys, usr, resp.Text, resp.Usage.CompletionTokens, parseRepaired)
		diag.PromptTokens = int(resp.Usage.PromptTokens)
		diag.ErrorType = "parse_error"
		diag.ErrorMessage = perr.Error()
		return nil, &PruneError{Sentinel: fmt.Errorf("%w: %w", ErrPruneParseFailed, perr), Diagnostics: diag}
	}
	if len(parseRepaired) > 0 {
		s.logger.Info("prune response repaired", "ops", parseRepaired)
	}

	preview := computePruneDiff(entries, refined)
	preview.Diagnostics = s.buildDiagnostics(backendRecord, tmpl, start, sys, usr, resp.Text, resp.Usage.CompletionTokens, parseRepaired)
	preview.Diagnostics.PromptTokens = int(resp.Usage.PromptTokens)
	preview.Diagnostics.ParsedCount = len(refined)

	s.logger.Debug("prune preview ok",
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"parsed_count", len(refined),
		"to_delete", preview.ToDelete,
		"to_update", preview.ToUpdate,
		"to_keep", preview.ToKeep)

	return preview, nil
}

// buildDiagnostics 构建 PruneDiagnostics，复用 SSE 的截断逻辑。
func (s *GlossaryPruneService) buildDiagnostics(backendRecord *BackendRecord, tmpl *ent.PrunePromptTemplate, start time.Time, system, user, received string, completionTokens int64, repaired []string) PruneDiagnostics {
	sysTrunc, sysTruncated, sysLen := progress.TruncateSSEContent(system)
	usrTrunc, usrTruncated, usrLen := progress.TruncateSSEContent(user)
	recvTrunc, recvTruncated, recvLen := progress.TruncateSSEContent(received)
	return PruneDiagnostics{
		BackendName:      backendRecord.Name,
		TemplateName:     tmpl.Name,
		DurationMs:       int(time.Since(start).Milliseconds()),
		CompletionTokens: int(completionTokens),
		SystemPrompt:     sysTrunc,
		UserMessage:      usrTrunc,
		SystemTruncated:  sysTruncated,
		UserTruncated:    usrTruncated,
		SystemLength:     sysLen,
		UserLength:       usrLen,
		ReceivedContent:  recvTrunc,
		RecvTruncated:    recvTruncated,
		RecvLength:       recvLen,
		RepairedOps:      repaired,
	}
}

// extractHTTPStatus 从错误中提取 HTTP 状态码（若可提取）。
func extractHTTPStatus(err error) int {
	if hsErr, ok := err.(backend.HTTPStatusError); ok {
		return hsErr.HTTPStatus()
	}
	if code, ok := backend.ExtractHTTPStatusCode(err.Error()); ok {
		return code
	}
	return 0
}

// computePruneDiff 计算现有术语表与 LLM 精炼结果之间的差异。
func computePruneDiff(existing []*ent.GlossaryEntry, refined []prompt.BootstrapEntry) *PrunePreview {
	type refinedEntry struct {
		Target string
		Notes  string
	}
	refinedIdx := make(map[string]refinedEntry, len(refined))
	var collisions []string
	for _, r := range refined {
		key := strings.ToLower(strings.TrimSpace(r.Source))
		if key == "" {
			continue
		}
		if _, exists := refinedIdx[key]; exists {
			collisions = append(collisions, r.Source)
			continue
		}
		refinedIdx[key] = refinedEntry{Target: r.Target, Notes: r.Notes}
	}
	if len(collisions) > 0 {
		slog.Warn("prune diff: duplicate refined sources detected (case-insensitive), keeping first occurrence",
			"collisions", collisions)
	}

	preview := &PrunePreview{
		Suggestions: []PruneSuggestion{},
		Total:       len(existing),
	}

	for _, e := range existing {
		key := strings.ToLower(strings.TrimSpace(e.Source))
		r, ok := refinedIdx[key]
		if !ok {
			preview.Suggestions = append(preview.Suggestions, PruneSuggestion{
				EntryID:       e.ID,
				Action:        "delete",
				Source:        e.Source,
				OldTarget:     e.Target,
				OldNotes:      e.Notes,
				TargetChanged: false,
				NotesChanged:  false,
			})
			preview.ToDelete++
			continue
		}
		targetChanged := r.Target != e.Target
		notesChanged := r.Notes != e.Notes
		if targetChanged || notesChanged {
			preview.Suggestions = append(preview.Suggestions, PruneSuggestion{
				EntryID:       e.ID,
				Action:        "update",
				Source:        e.Source,
				OldTarget:     e.Target,
				NewTarget:     r.Target,
				OldNotes:      e.Notes,
				NewNotes:      r.Notes,
				TargetChanged: targetChanged,
				NotesChanged:  notesChanged,
			})
			preview.ToUpdate++
		} else {
			preview.ToKeep++
		}
	}

	return preview
}

// Apply 应用用户选中的精简变更（逐条处理，单条失败不中断其余）。
func (s *GlossaryPruneService) Apply(ctx context.Context, actorUserID, projectID int, changes []PruneChange) (*PruneApplyResult, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}

	result := &PruneApplyResult{}
	for _, ch := range changes {
		switch ch.Action {
		case "delete":
			if err := s.applyDelete(ctx, projectID, ch.EntryID); err != nil {
				s.logger.Warn("prune apply delete failed", "entry_id", ch.EntryID, "action", "delete", "err", err)
				result.Failed++
				continue
			}
			result.Deleted++
		case "update":
			if err := s.applyUpdate(ctx, actorUserID, projectID, ch.EntryID, ch.Target, ch.Notes); err != nil {
				s.logger.Warn("prune apply update failed", "entry_id", ch.EntryID, "action", "update", "err", err)
				result.Failed++
				continue
			}
			result.Updated++
		default:
			s.logger.Warn("prune apply unknown action", "entry_id", ch.EntryID, "action", ch.Action)
			result.Failed++
		}
	}

	s.logger.Info("prune apply completed",
		"deleted", result.Deleted,
		"updated", result.Updated,
		"failed", result.Failed)

	return result, nil
}

// applyDelete 删除指定条目（含关联 SyncTask），不校验权限（已在 Apply 中完成）。
func (s *GlossaryPruneService) applyDelete(ctx context.Context, projectID, entryID int) error {
	// 先验证条目属于该项目，避免跨项目删除 SyncTask
	entry, err := s.client.GlossaryEntry.Query().
		Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrGlossaryEntryNotFound
		}
		return err
	}
	// 删除关联的 SyncTask（带 entry_id 限定）
	if _, err := s.client.SyncTask.Delete().
		Where(synctask.EntryIDEQ(entry.ID)).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete sync tasks for entry %d: %w", entry.ID, err)
	}
	deleted, err := s.client.GlossaryEntry.Delete().
		Where(glossaryentry.IDEQ(entry.ID), glossaryentry.ProjectIDEQ(projectID)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrGlossaryEntryNotFound
	}
	return nil
}

// applyUpdate 更新指定条目的 target/notes（保留 source 和 case_sensitive）。
// 复用 GlossaryService.UpdateEntry 以共享标准化逻辑与 target_changed 信号。
func (s *GlossaryPruneService) applyUpdate(ctx context.Context, actorUserID, projectID, entryID int, target, notes string) error {
	// 先加载条目获取 source 和 case_sensitive（不修改这两个字段）
	entry, err := s.client.GlossaryEntry.Query().
		Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrGlossaryEntryNotFound
		}
		return err
	}
	_, err = s.glossary.UpdateEntry(ctx, actorUserID, projectID, entryID, GlossaryEntryInput{
		Source:        entry.Source,
		Target:        target,
		CaseSensitive: entry.CaseSensitive,
		Notes:         notes,
	})
	return err
}

// headSnippetLocal 截取字符串前 n 个字符（与 pipeline.headSnippet 同逻辑）。
func headSnippetLocal(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
