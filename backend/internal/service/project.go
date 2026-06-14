package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/projectbackend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/stagebackendoverride"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/tmentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationjob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

const (
	ProjectResourceScopeProject      = "project"
	ProjectResourceScopeOrganization = "organization"

	StageTranslate = "translate"
	StageBootstrap = "bootstrap"

	BackendModeInherit = "inherit"
)

var (
	ErrProjectNotFound      = errors.New("project not found")
	ErrProjectOwnerConflict = errors.New("project owner conflict")
	ErrResourceScopeInvalid = errors.New("resource scope invalid")
	ErrStageInvalid         = errors.New("stage invalid")
	ErrBackendModeInvalid   = errors.New("backend mode invalid")
	ErrBackendOrderInvalid  = errors.New("backend order invalid")
)

type ProjectService struct {
	client   *ent.Client
	users    *UserService
	backends *BackendService
}

type CreateProjectInput struct {
	Name                     string
	OwnerUserID              *int
	OwnerOrgID               *int
	ResourceScope            string
	Config                   map[string]any
	DefaultTranslationConfig map[string]any
	SourceLang               string
	TargetLang               string
}

type UpdateProjectInput struct {
	Name                     string
	ResourceScope            string
	Config                   map[string]any
	DefaultTranslationConfig map[string]any
	SourceLang               string
	TargetLang               string
}

type ProjectBackendBindingInput struct {
	BackendID int
}

type ProjectBackendBinding struct {
	OrderIndex int
	BackendID  int
	Name       string
	Type       string
	Priority   int
	Options    map[string]any
}

type StageBackendOverrideInput struct {
	Stage        string
	BackendMode  string
	BackendOrder []string
}

type StageBackendOverrideView struct {
	Stage        string
	BackendMode  string
	BackendOrder []string
}

type ProjectBackendSettings struct {
	Backends       []ProjectBackendBinding
	StageOverrides map[string]StageBackendOverrideView
}

func NewProjectService(client *ent.Client, users *UserService, backends *BackendService) *ProjectService {
	return &ProjectService{client: client, users: users, backends: backends}
}

func (s *ProjectService) CreateProject(ctx context.Context, actorUserID int, input CreateProjectInput) (*ent.Project, error) {
	normalized, err := s.normalizeCreateInput(ctx, actorUserID, input)
	if err != nil {
		return nil, err
	}
	create := s.client.Project.Create().
		SetName(normalized.Name).
		SetResourceScope(normalized.ResourceScope).
		SetConfig(cloneMap(normalized.Config)).
		SetDefaultTranslationConfig(cloneMap(normalized.DefaultTranslationConfig)).
		SetSourceLang(normalized.SourceLang).
		SetTargetLang(normalized.TargetLang)
	if normalized.OwnerUserID != nil {
		create.SetOwnerUserID(*normalized.OwnerUserID)
	}
	if normalized.OwnerOrgID != nil {
		create.SetOwnerOrgID(*normalized.OwnerOrgID)
	}
	created, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrInvalidInput
		}
		return nil, err
	}
	return created, nil
}

func (s *ProjectService) ListProjectsForUser(ctx context.Context, actorUserID int) ([]*ent.Project, error) {
	return s.client.Project.Query().
		Where(project.Or(
			project.OwnerUserIDEQ(actorUserID),
			project.HasOwnerOrgWith(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actorUserID)))),
		)).
		Order(ent.Asc(project.FieldID)).
		All(ctx)
}

func (s *ProjectService) GetProject(ctx context.Context, actorUserID, projectID int) (*ent.Project, error) {
	return s.requireProjectAccess(ctx, actorUserID, projectID, false)
}

func (s *ProjectService) UpdateProject(ctx context.Context, actorUserID, projectID int, input UpdateProjectInput) (*ent.Project, error) {
	current, err := s.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}
	normalized, err := s.normalizeUpdateInput(current, input)
	if err != nil {
		return nil, err
	}
	updated, err := s.client.Project.UpdateOneID(projectID).
		SetName(normalized.Name).
		SetResourceScope(normalized.ResourceScope).
		SetConfig(cloneMap(normalized.Config)).
		SetDefaultTranslationConfig(cloneMap(normalized.DefaultTranslationConfig)).
		SetSourceLang(normalized.SourceLang).
		SetTargetLang(normalized.TargetLang).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrProjectNotFound
		}
		return nil, err
	}
	return updated, nil
}

func (s *ProjectService) DeleteProject(ctx context.Context, actorUserID, projectID int) ([]string, error) {
	if _, err := s.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}
	return s.cascadeDeleteProject(ctx, projectID)
}

// cascadeDeleteProject 在事务中执行项目级联删除，返回需要清理的物理文件存储路径列表。
// 删除顺序遵循依赖关系：叶子节点优先，最后删除项目本身。
func (s *ProjectService) cascadeDeleteProject(ctx context.Context, projectID int) (storagePaths []string, err error) {
	// 1. 收集需要删除文件的 Resource 存储路径
	resources, err := s.client.Resource.Query().
		Where(resource.ProjectIDEQ(projectID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query project resources: %w", err)
	}
	for _, r := range resources {
		if r.StoragePath != "" {
			storagePaths = append(storagePaths, r.StoragePath)
		}
	}

	// 2. 开启事务执行级联删除
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// 收集项目关联的 TranslationJob IDs（用于删除 JobResource）
	tjIDs, err := tx.TranslationJob.Query().
		Where(translationjob.HasProjectWith(project.IDEQ(projectID))).
		IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("query translation job IDs: %w", err)
	}

	// 收集项目关联的 Resource IDs（用于删除 Segment 和 JobResource）
	resIDs := make([]int, 0, len(resources))
	for _, r := range resources {
		resIDs = append(resIDs, r.ID)
	}

	// Step 1: 删除 JobResource（同时依赖 TJ 和 Resource）
	if len(tjIDs) > 0 || len(resIDs) > 0 {
		var preds []predicate.JobResource
		if len(tjIDs) > 0 {
			preds = append(preds, jobresource.HasJobWith(translationjob.IDIn(tjIDs...)))
		}
		if len(resIDs) > 0 {
			preds = append(preds, jobresource.HasResourceWith(resource.IDIn(resIDs...)))
		}
		_, err = tx.JobResource.Delete().
			Where(jobresource.Or(preds...)).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("delete job resources: %w", err)
		}
	}

	// Step 2: 删除 TranslationJob
	if len(tjIDs) > 0 {
		_, err = tx.TranslationJob.Delete().
			Where(translationjob.IDIn(tjIDs...)).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("delete translation jobs: %w", err)
		}
	}

	// Step 4: 删除 Segment（依赖 Resource）
	if len(resIDs) > 0 {
		_, err = tx.Segment.Delete().
			Where(segment.ResourceIDIn(resIDs...)).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("delete segments: %w", err)
		}
	}

	// Step 5: 删除 Resource DB 记录
	if len(resIDs) > 0 {
		_, err = tx.Resource.Delete().
			Where(resource.IDIn(resIDs...)).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("delete resources: %w", err)
		}
	}

	// Step 6: 删除 ProjectBackend
	_, err = tx.ProjectBackend.Delete().
		Where(projectbackend.HasProjectWith(project.IDEQ(projectID))).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete project backends: %w", err)
	}

	// Step 8: 删除 StageBackendOverride
	_, err = tx.StageBackendOverride.Delete().
		Where(stagebackendoverride.HasProjectWith(project.IDEQ(projectID))).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete stage backend overrides: %w", err)
	}

	// Step 9: 删除 GlossaryEntry
	_, err = tx.GlossaryEntry.Delete().
		Where(glossaryentry.HasProjectWith(project.IDEQ(projectID))).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete glossary entries: %w", err)
	}

	// Step 10: 删除 TMEntry
	_, err = tx.TMEntry.Delete().
		Where(tmentry.HasProjectWith(project.IDEQ(projectID))).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete tm entries: %w", err)
	}

	// Step 11-12: ActivityLog 和 UsageRecord 保留，SetNull 由 FK 策略自动处理

	// Step 13: 删除 Project
	err = tx.Project.DeleteOneID(projectID).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete project: %w", err)
	}

	// 提交事务
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return storagePaths, nil
}

func (s *ProjectService) SetBackendOrder(ctx context.Context, actorUserID, projectID int, bindings []ProjectBackendBindingInput) ([]ProjectBackendBinding, error) {
	projectRow, err := s.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}
	accessible, err := s.backends.resolveAccessibleBackends(ctx, projectRow)
	if err != nil {
		return nil, err
	}
	resolved, err := validateBindingInputs(bindings, accessible)
	if err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ProjectBackend.Delete().Where(projectbackend.HasProjectWith(project.IDEQ(projectID))).Exec(ctx); err != nil {
		return nil, err
	}
	for i, item := range resolved {
		if _, err = tx.ProjectBackend.Create().
			SetProjectID(projectID).
			SetOrderIndex(i).
			SetBackendID(item.ID).
			Save(ctx); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return buildBindingViews(resolved), nil
}

func (s *ProjectService) GetBackendSettings(ctx context.Context, actorUserID, projectID int) (*ProjectBackendSettings, error) {
	projectRow, err := s.requireProjectAccess(ctx, actorUserID, projectID, false)
	if err != nil {
		return nil, err
	}
	bindings, err := s.loadProjectBindings(ctx, projectRow)
	if err != nil {
		return nil, err
	}
	overrides, err := s.loadStageOverrides(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &ProjectBackendSettings{Backends: bindings, StageOverrides: overrides}, nil
}

func (s *ProjectService) SetStageBackendOverride(ctx context.Context, actorUserID, projectID int, input StageBackendOverrideInput) (*StageBackendOverrideView, error) {
	projectRow, err := s.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}
	stage, err := normalizeStage(input.Stage)
	if err != nil {
		return nil, err
	}
	mode, err := normalizeBackendMode(input.BackendMode)
	if err != nil {
		return nil, err
	}
	accessible, err := s.backends.resolveAccessibleBackends(ctx, projectRow)
	if err != nil {
		return nil, err
	}
	order, err := validateOverrideOrder(mode, input.BackendOrder, accessible)
	if err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.StageBackendOverride.Delete().
		Where(stagebackendoverride.HasProjectWith(project.IDEQ(projectID)), stagebackendoverride.StageEQ(stagebackendoverride.Stage(stage))).
		Exec(ctx); err != nil {
		return nil, err
	}
	created, err := tx.StageBackendOverride.Create().
		SetProjectID(projectID).
		SetStage(stagebackendoverride.Stage(stage)).
		SetBackendMode(stagebackendoverride.BackendMode(mode)).
		SetBackendOrder(cloneStrings(order)).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &StageBackendOverrideView{Stage: string(created.Stage), BackendMode: string(created.BackendMode), BackendOrder: cloneStrings(created.BackendOrder)}, nil
}

func (s *ProjectService) ResolveStagePlan(ctx context.Context, actorUserID, projectID int, stage string) ([]ProjectBackendBinding, error) {
	projectRow, err := s.requireProjectAccess(ctx, actorUserID, projectID, false)
	if err != nil {
		return nil, err
	}
	normalizedStage, err := normalizeStage(stage)
	if err != nil {
		return nil, err
	}
	accessible, err := s.backends.resolveAccessibleBackends(ctx, projectRow)
	if err != nil {
		return nil, err
	}
	defaultBindings, err := s.loadProjectBindings(ctx, projectRow)
	if err != nil {
		return nil, err
	}
	override, err := s.client.StageBackendOverride.Query().
		Where(stagebackendoverride.HasProjectWith(project.IDEQ(projectID)), stagebackendoverride.StageEQ(stagebackendoverride.Stage(normalizedStage))).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if ent.IsNotFound(err) || string(override.BackendMode) == BackendModeInherit {
		return defaultBindings, nil
	}
	byName, err := uniqueBackendNameMap(accessible)
	if err != nil {
		return nil, err
	}
	selected := make([]*BackendRecord, 0, len(override.BackendOrder))
	for _, name := range override.BackendOrder {
		item, ok := byName[name]
		if !ok {
			return nil, ErrBackendOrderInvalid
		}
		selected = append(selected, item)
	}
	if string(override.BackendMode) == "restrict" {
		return buildBindingViews(selected), nil
	}
	used := make(map[int]struct{}, len(selected))
	for _, item := range selected {
		used[item.ID] = struct{}{}
	}
	for _, binding := range defaultBindings {
		if _, ok := used[binding.BackendID]; ok {
			continue
		}
		selected = append(selected, &BackendRecord{
			ID:       binding.BackendID,
			Name:     binding.Name,
			Type:     binding.Type,
			Priority: binding.Priority,
			Options:  cloneMap(binding.Options),
		})
	}
	return buildBindingViews(selected), nil
}

func (s *ProjectService) requireProjectAccess(ctx context.Context, actorUserID, projectID int, write bool) (*ent.Project, error) {
	projectRow, err := s.client.Project.Get(ctx, projectID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrProjectNotFound
		}
		return nil, err
	}
	switch {
	case projectRow.OwnerUserID != nil:
		if *projectRow.OwnerUserID != actorUserID {
			return nil, ErrForbidden
		}
	case projectRow.OwnerOrgID != nil:
		minRole := OrgRoleMember
		if write {
			minRole = OrgRoleAdmin
		}
		if _, err := s.users.requireMembership(ctx, actorUserID, *projectRow.OwnerOrgID, minRole); err != nil {
			return nil, err
		}
	default:
		return nil, ErrProjectOwnerConflict
	}
	return projectRow, nil
}

func (s *ProjectService) normalizeCreateInput(ctx context.Context, actorUserID int, input CreateProjectInput) (CreateProjectInput, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreateProjectInput{}, ErrInvalidInput
	}
	ownerUserID := input.OwnerUserID
	ownerOrgID := input.OwnerOrgID
	if ownerUserID == nil && ownerOrgID == nil {
		ownerUserID = &actorUserID
	}
	if ownerUserID != nil && ownerOrgID != nil {
		return CreateProjectInput{}, ErrProjectOwnerConflict
	}
	if ownerUserID != nil && *ownerUserID != actorUserID {
		return CreateProjectInput{}, ErrForbidden
	}
	if ownerOrgID != nil {
		if _, err := s.users.requireMembership(ctx, actorUserID, *ownerOrgID, OrgRoleAdmin); err != nil {
			return CreateProjectInput{}, err
		}
	}
	scope, err := normalizeResourceScope(input.ResourceScope, ownerOrgID != nil)
	if err != nil {
		return CreateProjectInput{}, err
	}
	return CreateProjectInput{
		Name:                     name,
		OwnerUserID:              ownerUserID,
		OwnerOrgID:               ownerOrgID,
		ResourceScope:            scope,
		Config:                   cloneMap(input.Config),
		DefaultTranslationConfig: cloneMap(input.DefaultTranslationConfig),
		SourceLang:               normalizeLangOrDefault(input.SourceLang, "auto"),
		TargetLang:               normalizeLangOrDefault(input.TargetLang, "zh"),
	}, nil
}

func (s *ProjectService) normalizeUpdateInput(current *ent.Project, input UpdateProjectInput) (UpdateProjectInput, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = current.Name
	}
	scope, err := normalizeResourceScope(input.ResourceScope, current.OwnerOrgID != nil)
	if err != nil {
		return UpdateProjectInput{}, err
	}
	configValue := cloneMap(input.Config)
	if len(configValue) == 0 {
		configValue = cloneMap(current.Config)
	}
	defaultTranslationConfig := cloneMap(input.DefaultTranslationConfig)
	if len(defaultTranslationConfig) == 0 {
		defaultTranslationConfig = cloneMap(current.DefaultTranslationConfig)
	}
	return UpdateProjectInput{
		Name:                     name,
		ResourceScope:            scope,
		Config:                   configValue,
		DefaultTranslationConfig: defaultTranslationConfig,
		SourceLang:               normalizeLangOrDefault(input.SourceLang, current.SourceLang),
		TargetLang:               normalizeLangOrDefault(input.TargetLang, current.TargetLang),
	}, nil
}

func (s *ProjectService) loadProjectBindings(ctx context.Context, projectRow *ent.Project) ([]ProjectBackendBinding, error) {
	rows, err := s.client.ProjectBackend.Query().
		Where(projectbackend.HasProjectWith(project.IDEQ(projectRow.ID))).
		Order(ent.Asc(projectbackend.FieldOrderIndex), ent.Asc(projectbackend.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	accessible, err := s.backends.resolveAccessibleBackends(ctx, projectRow)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*BackendRecord, len(accessible))
	for _, item := range accessible {
		byID[item.ID] = item
	}
	out := make([]ProjectBackendBinding, 0, len(rows))
	for _, row := range rows {
		item, ok := byID[row.BackendID]
		if !ok {
			return nil, ErrBackendNotFound
		}
		out = append(out, ProjectBackendBinding{
			OrderIndex: row.OrderIndex,
			BackendID:  item.ID,
			Name:       item.Name,
			Type:       item.Type,
			Priority:   item.Priority,
			Options:    cloneMap(item.Options),
		})
	}
	return out, nil
}

func (s *ProjectService) loadStageOverrides(ctx context.Context, projectID int) (map[string]StageBackendOverrideView, error) {
	rows, err := s.client.StageBackendOverride.Query().
		Where(stagebackendoverride.HasProjectWith(project.IDEQ(projectID))).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]StageBackendOverrideView, len(rows))
	for _, row := range rows {
		out[string(row.Stage)] = StageBackendOverrideView{
			Stage:        string(row.Stage),
			BackendMode:  string(row.BackendMode),
			BackendOrder: cloneStrings(row.BackendOrder),
		}
	}
	return out, nil
}

func validateBindingInputs(bindings []ProjectBackendBindingInput, accessible []*BackendRecord) ([]*BackendRecord, error) {
	if len(bindings) == 0 {
		return []*BackendRecord{}, nil
	}
	byID := make(map[int]*BackendRecord, len(accessible))
	for _, item := range accessible {
		byID[item.ID] = item
	}
	seen := make(map[int]struct{}, len(bindings))
	out := make([]*BackendRecord, 0, len(bindings))
	for _, binding := range bindings {
		if _, dup := seen[binding.BackendID]; dup {
			return nil, ErrBackendOrderInvalid
		}
		item, ok := byID[binding.BackendID]
		if !ok {
			return nil, ErrBackendNotFound
		}
		seen[binding.BackendID] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

func validateOverrideOrder(mode string, names []string, accessible []*BackendRecord) ([]string, error) {
	if mode == BackendModeInherit {
		return []string{}, nil
	}
	byName, err := uniqueBackendNameMap(accessible)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, ErrBackendOrderInvalid
		}
		if _, ok := byName[name]; !ok {
			return nil, ErrBackendOrderInvalid
		}
		if _, dup := seen[name]; dup {
			return nil, ErrBackendOrderInvalid
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

func uniqueBackendNameMap(accessible []*BackendRecord) (map[string]*BackendRecord, error) {
	byName := make(map[string]*BackendRecord, len(accessible))
	for _, item := range accessible {
		if existing, ok := byName[item.Name]; ok {
			if existing.ID != item.ID {
				return nil, ErrBackendNameAmbiguous
			}
		}
		byName[item.Name] = item
	}
	return byName, nil
}

func buildBindingViews(in []*BackendRecord) []ProjectBackendBinding {
	out := make([]ProjectBackendBinding, 0, len(in))
	for i, item := range in {
		out = append(out, ProjectBackendBinding{
			OrderIndex: i,
			BackendID:  item.ID,
			Name:       item.Name,
			Type:       item.Type,
			Priority:   item.Priority,
			Options:    cloneMap(item.Options),
		})
	}
	return out
}

func normalizeResourceScope(raw string, orgOwned bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		if orgOwned {
			return ProjectResourceScopeOrganization, nil
		}
		return ProjectResourceScopeProject, nil
	case ProjectResourceScopeProject:
		return ProjectResourceScopeProject, nil
	case ProjectResourceScopeOrganization:
		if !orgOwned {
			return "", ErrResourceScopeInvalid
		}
		return ProjectResourceScopeOrganization, nil
	default:
		return "", ErrResourceScopeInvalid
	}
}

func normalizeStage(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case StageTranslate:
		return StageTranslate, nil
	case StageBootstrap:
		return StageBootstrap, nil
	default:
		return "", ErrStageInvalid
	}
}

func normalizeBackendMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", BackendModeInherit:
		return BackendModeInherit, nil
	case "prepend":
		return "prepend", nil
	case "restrict":
		return "restrict", nil
	default:
		return "", ErrBackendModeInvalid
	}
}

func normalizeLangOrDefault(raw, fallback string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fallback
	}
	return v
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
