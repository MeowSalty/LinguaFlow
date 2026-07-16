package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrPruneLLMCallFailed = errors.New("prune: llm call failed")
	ErrPruneParseFailed   = errors.New("prune: parse response failed")
)

// PruneSuggestion 是 Preview 返回的单条精简建议。
type PruneSuggestion struct {
	EntryID   int    `json:"entry_id"`
	Action    string `json:"action"` // "delete" | "update"
	Source    string `json:"source"`
	OldTarget string `json:"old_target"`
	NewTarget string `json:"new_target,omitempty"`
	OldNotes  string `json:"old_notes"`
	NewNotes  string `json:"new_notes,omitempty"`
}

// PrunePreview 是 Preview 的返回结构。
type PrunePreview struct {
	Suggestions []PruneSuggestion `json:"suggestions"`
	Total       int               `json:"total"`
	ToDelete    int               `json:"to_delete"`
	ToUpdate    int               `json:"to_update"`
	ToKeep      int               `json:"to_keep"`
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
	pruneTemplates *PrunePromptTemplateService
	limiterPool    *backend.LimiterPool
	logger         *slog.Logger
}

// NewGlossaryPruneService 创建 GlossaryPruneService 实例。
func NewGlossaryPruneService(
	client *ent.Client,
	projects *ProjectService,
	backends *BackendService,
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

	s.logger.Info("prune preview started",
		"project_id", projectID,
		"entry_count", len(entries),
		"backend_name", backendRecord.Name,
		"template_name", tmpl.Name)

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
		return nil, fmt.Errorf("%w: %w", ErrPruneLLMCallFailed, callErr)
	}

	repairOpts := defaultPruneRepairOptions()
	refined, parseRepaired, perr := repair.TryRepairBootstrap(resp.Text, repairOpts)
	if perr != nil {
		s.logger.Warn("prune parse failed",
			"resp_len", len(resp.Text),
			"resp_head", headSnippetLocal(resp.Text, 200),
			"repaired", parseRepaired)
		return nil, fmt.Errorf("%w: %w", ErrPruneParseFailed, perr)
	}
	if len(parseRepaired) > 0 {
		s.logger.Info("prune response repaired", "ops", parseRepaired)
	}

	preview := computePruneDiff(entries, refined)

	s.logger.Debug("prune preview ok",
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"parsed_count", len(refined),
		"to_delete", preview.ToDelete,
		"to_update", preview.ToUpdate,
		"to_keep", preview.ToKeep)

	return preview, nil
}

// computePruneDiff 计算现有术语表与 LLM 精炼结果之间的差异。
func computePruneDiff(existing []*ent.GlossaryEntry, refined []prompt.BootstrapEntry) *PrunePreview {
	type refinedEntry struct {
		Target string
		Notes  string
	}
	refinedIdx := make(map[string]refinedEntry, len(refined))
	for _, r := range refined {
		key := strings.ToLower(strings.TrimSpace(r.Source))
		if key == "" {
			continue
		}
		refinedIdx[key] = refinedEntry{Target: r.Target, Notes: r.Notes}
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
				EntryID:   e.ID,
				Action:    "delete",
				Source:    e.Source,
				OldTarget: e.Target,
				OldNotes:  e.Notes,
			})
			preview.ToDelete++
			continue
		}
		if r.Target != e.Target || r.Notes != e.Notes {
			preview.Suggestions = append(preview.Suggestions, PruneSuggestion{
				EntryID:   e.ID,
				Action:    "update",
				Source:    e.Source,
				OldTarget: e.Target,
				NewTarget: r.Target,
				OldNotes:  e.Notes,
				NewNotes:  r.Notes,
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
			if err := s.applyUpdate(ctx, projectID, ch.EntryID, ch.Target, ch.Notes); err != nil {
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
	if _, err := s.client.SyncTask.Delete().
		Where(synctask.EntryIDEQ(entryID)).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete sync tasks for entry %d: %w", entryID, err)
	}
	deleted, err := s.client.GlossaryEntry.Delete().
		Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID)).
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
func (s *GlossaryPruneService) applyUpdate(ctx context.Context, projectID, entryID int, target, notes string) error {
	entry, err := s.client.GlossaryEntry.Query().
		Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrGlossaryEntryNotFound
		}
		return err
	}
	_, err = s.client.GlossaryEntry.UpdateOneID(entry.ID).
		SetTarget(strings.TrimSpace(target)).
		SetNotes(strings.TrimSpace(notes)).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return ErrGlossaryEntryExists
		}
		return err
	}
	return nil
}

// headSnippetLocal 截取字符串前 n 个字符（与 pipeline.headSnippet 同逻辑）。
func headSnippetLocal(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
