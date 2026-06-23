package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobevent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/tmentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationjob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

const (
	ProjectResourceScopeProject      = "project"
	ProjectResourceScopeOrganization = "organization"
)

var (
	ErrProjectNotFound      = errors.New("project not found")
	ErrProjectOwnerConflict = errors.New("project owner conflict")
	ErrResourceScopeInvalid = errors.New("resource scope invalid")
)

type ProjectService struct {
	client *ent.Client
	users  *UserService
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

func NewProjectService(client *ent.Client, users *UserService) *ProjectService {
	return &ProjectService{client: client, users: users}
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

	// Step 2: 删除 JobEvent（依赖 TranslationJob，NoAction）
	if len(tjIDs) > 0 {
		_, err = tx.JobEvent.Delete().
			Where(jobevent.HasJobWith(translationjob.IDIn(tjIDs...))).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("delete job events: %w", err)
		}
	}

	// Step 3: 删除 TranslationJob
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

	// Step 6: 删除 SyncTask（必须在 GlossaryEntry 和 Project 之前，因为它同时依赖两者）
	_, err = tx.SyncTask.Delete().
		Where(synctask.ProjectIDEQ(projectID)).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete sync tasks: %w", err)
	}

	// Step 7: 删除 GlossaryEntry
	_, err = tx.GlossaryEntry.Delete().
		Where(glossaryentry.HasProjectWith(project.IDEQ(projectID))).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete glossary entries: %w", err)
	}

	// Step 8: 删除 TMEntry
	_, err = tx.TMEntry.Delete().
		Where(tmentry.HasProjectWith(project.IDEQ(projectID))).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete tm entries: %w", err)
	}

	// ActivityLog 保留，SetNull 由 FK 策略自动处理

	// Step 9: 删除 Project
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
