package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sseevent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/tmentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

var (
	ErrProjectNotFound      = errors.New("project not found")
	ErrProjectOwnerConflict = errors.New("project owner conflict")
)

type ProjectService struct {
	client  *ent.Client
	users   *UserService
	storage *StorageService
}

type CreateProjectInput struct {
	StorageSpaceID  *int
	Name            string
	OwnerUserID     *int
	OwnerOrgID      *int
	Config          map[string]any
	GlossaryEnabled *bool
	SourceLang      string
	TargetLang      string
}

type UpdateProjectInput struct {
	Name            string
	Config          map[string]any
	GlossaryEnabled *bool
	SourceLang      string
	TargetLang      string
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
		SetConfig(cloneMap(normalized.Config)).
		SetGlossaryEnabled(normalized.GlossaryEnabled != nil && *normalized.GlossaryEnabled).
		SetSourceLang(normalized.SourceLang).
		SetTargetLang(normalized.TargetLang)
	if normalized.OwnerUserID != nil {
		create.SetOwnerUserID(*normalized.OwnerUserID)
	}
	if s.storage != nil {
		id, e := s.storage.selectSpace(ctx, s.client, &ent.Project{OwnerUserID: normalized.OwnerUserID}, input.StorageSpaceID)
		if e != nil {
			return nil, e
		}
		create.SetStorageSpaceID(id)
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

// CreateOrgProject 创建组织项目。
// 鉴权与审计共享该组织串行化的变更事务。
func (s *ProjectService) CreateOrgProject(ctx context.Context, actorUserID, orgID int, input CreateProjectInput) (*ent.Project, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, ErrInvalidInput
	}
	var created *ent.Project
	err := withOrganizationMutation(ctx, s.client, orgID, func(client *ent.Client) error {
		if _, err := requireOrganizationMembership(ctx, client, actorUserID, orgID, OrgRoleAdmin); err != nil {
			return err
		}
		create := client.Project.Create().
			SetName(name).
			SetOwnerOrgID(orgID).
			SetConfig(cloneMap(input.Config)).
			SetGlossaryEnabled(input.GlossaryEnabled != nil && *input.GlossaryEnabled).
			SetSourceLang(normalizeLangOrDefault(input.SourceLang, "auto")).
			SetTargetLang(normalizeLangOrDefault(input.TargetLang, "zh"))
		var err error
		if s.storage != nil {
			id, e := s.storage.selectSpace(ctx, client, &ent.Project{OwnerOrgID: &orgID}, input.StorageSpaceID)
			if e != nil {
				return e
			}
			create.SetStorageSpaceID(id)
		}
		created, err = create.Save(ctx)
		if err != nil {
			return err
		}
		return recordAuditEvent(ctx, client, AuditEvent{ActorUserID: actorUserID, ProjectID: &created.ID,
			Action: "project.create", ResourceType: "project", ResourceID: created.ID})
	})
	if err != nil {
		return nil, err
	}
	return created.Unwrap(), nil
}

func (s *ProjectService) ListProjectsForUser(ctx context.Context, actorUserID int) ([]*ent.Project, error) {
	return s.client.Project.Query().
		Where(readableProjectPredicate(actorUserID)).
		Order(ent.Asc(project.FieldID)).
		All(ctx)
}

// readableProjectPredicate 为读取镜像 requireProjectAccess，包括
// 在畸形数据同时设置两种 owner 时个人属主的优先。
// 将权限保留在发现类查询内，使过滤先于分页。
func readableProjectPredicate(actorUserID int) predicate.Project {
	return project.Or(
		project.OwnerUserIDEQ(actorUserID),
		project.And(
			project.OwnerUserIDIsNil(),
			project.HasOwnerOrgWith(organization.HasMembershipsWith(
				orgmembership.HasUserWith(user.IDEQ(actorUserID)),
				orgmembership.RoleIn(OrgRoleMember, OrgRoleAdmin, OrgRoleOwner),
			)),
		),
	)
}

// ListOrgProjects 列出指定组织的所有项目。
// 即使畸形行同时带有 orgID，个人所有权仍优先。
func (s *ProjectService) ListOrgProjects(ctx context.Context, actorUserID, orgID int) ([]*ent.Project, error) {
	if _, err := requireOrganizationMembership(ctx, s.client, actorUserID, orgID, OrgRoleMember); err != nil {
		return nil, err
	}
	return s.client.Project.Query().
		Where(project.OwnerUserIDIsNil(), project.OwnerOrgIDEQ(orgID), readableProjectPredicate(actorUserID)).
		Order(ent.Asc(project.FieldID)).
		All(ctx)
}

func (s *ProjectService) GetProject(ctx context.Context, actorUserID, projectID int) (*ent.Project, error) {
	return s.requireProjectAccess(ctx, actorUserID, projectID, false)
}

func (s *ProjectService) UpdateProject(ctx context.Context, actorUserID, projectID int, input UpdateProjectInput) (*ent.Project, error) {
	var updated *ent.Project
	err := s.mutateProject(ctx, actorUserID, projectID, func(client *ent.Client, current *ent.Project) error {
		normalized, err := s.normalizeUpdateInput(current, input)
		if err != nil {
			return err
		}
		glossaryEnabled := normalized.GlossaryEnabled
		updated, err = client.Project.UpdateOneID(projectID).
			AddOutputGeneration(1).
			SetName(normalized.Name).
			SetConfig(cloneMap(normalized.Config)).
			SetGlossaryEnabled(glossaryEnabled != nil && *glossaryEnabled).
			SetSourceLang(normalized.SourceLang).
			SetTargetLang(normalized.TargetLang).
			Save(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return ErrProjectNotFound
			}
			return err
		}
		if EffectiveProjectOrgID(current) != nil {
			return recordAuditEvent(ctx, client, AuditEvent{ActorUserID: actorUserID, ProjectID: &projectID,
				Action: "project.update", ResourceType: "project", ResourceID: projectID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated.Unwrap(), nil
}

func (s *ProjectService) DeleteProject(ctx context.Context, actorUserID, projectID int) ([]string, error) {
	var paths []string
	err := s.mutateProject(ctx, actorUserID, projectID, func(client *ent.Client, current *ent.Project) error {
		if EffectiveProjectOrgID(current) != nil {
			if err := recordAuditEvent(ctx, client, AuditEvent{ActorUserID: actorUserID, ProjectID: &projectID,
				Action: "project.delete", ResourceType: "project", ResourceID: projectID}); err != nil {
				return err
			}
		}
		var err error
		paths, err = cascadeDeleteProject(ctx, client, current, s.storage)
		return err
	})
	return paths, err
}

func (s *ProjectService) mutateProject(ctx context.Context, actorUserID, projectID int, mutate func(*ent.Client, *ent.Project) error) error {
	current, err := s.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return err
	}
	apply := func(client *ent.Client) error {
		transactionService := NewProjectService(client, NewUserService(client, nil))
		row, err := transactionService.requireProjectAccess(ctx, actorUserID, projectID, true)
		if err != nil {
			return err
		}
		return mutate(client, row)
	}
	if orgID := EffectiveProjectOrgID(current); orgID != nil {
		return withOrganizationMutation(ctx, s.client, *orgID, apply)
	}
	return withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
		// 在鉴权读快照之前获取 SQLite 的写锁。
		if _, err := client.Project.Update().Where(project.IDEQ(projectID)).SetUpdatedAt(time.Now().UTC()).Save(ctx); err != nil {
			return err
		}
		return apply(client)
	})
}

// cascadeDeleteProject 在事务中执行项目级联删除，返回需要清理的物理文件存储路径列表。
// 删除顺序遵循依赖关系：叶子节点优先，最后删除项目本身。
func cascadeDeleteProject(ctx context.Context, tx *ent.Client, current *ent.Project, storageService *StorageService) (storagePaths []string, err error) {
	projectID := current.ID
	// 1. 收集需要删除文件的 Resource 存储路径
	resources, err := tx.Resource.Query().
		Where(resource.ProjectIDEQ(projectID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query project resources: %w", err)
	}
	for _, r := range resources {
		if err = registerResourceDeletion(ctx, tx, projectID, r.ID, storageService); err != nil {
			return nil, err
		}
	}

	// 收集项目关联的 Job IDs（用于删除 JobResource）
	tjIDs, err := tx.Job.Query().
		Where(job.ProjectIDEQ(projectID)).
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
			preds = append(preds, jobresource.HasJobWith(job.IDIn(tjIDs...)))
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

	// Step 1.5: 删除 SSEEvent（依赖 Job）
	if len(tjIDs) > 0 {
		_, err = tx.SSEEvent.Delete().
			Where(sseevent.JobIDIn(tjIDs...)).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("delete sse events: %w", err)
		}
	}

	// Step 2: 删除 Job
	if len(tjIDs) > 0 {
		_, err = tx.Job.Delete().
			Where(job.IDIn(tjIDs...)).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("delete translation jobs: %w", err)
		}
	}

	// Step 3: 删除 Segment（依赖 Resource）
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
		Where(glossaryentry.ProjectIDEQ(projectID)).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete glossary entries: %w", err)
	}

	// Step 8: 删除 TMEntry
	_, err = tx.TMEntry.Delete().
		Where(tmentry.ProjectIDEQ(projectID)).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete tm entries: %w", err)
	}

	// 保留有效的组织历史；已删除的个人项目绝不转为个人历史。
	activities := tx.ActivityLog.Update().Where(activitylog.HasProjectWith(project.IDEQ(projectID)),
		activitylog.VisibilityScopeEQ(activitylog.VisibilityScopeProject))
	usage := tx.UsageRecord.Update().Where(usagerecord.HasProjectWith(project.IDEQ(projectID)),
		usagerecord.VisibilityScopeEQ(usagerecord.VisibilityScopeProject))
	if orgID := EffectiveProjectOrgID(current); orgID != nil {
		activities.SetOrganizationID(*orgID).SetVisibilityScope(activitylog.VisibilityScopeOrganization)
		usage.SetOrganizationID(*orgID).SetVisibilityScope(usagerecord.VisibilityScopeOrganization)
	} else {
		activities.ClearOrganization()
		usage.ClearOrganization()
	}
	if _, err := activities.Save(ctx); err != nil {
		return nil, err
	}
	if _, err := usage.Save(ctx); err != nil {
		return nil, err
	}

	// Step 9: 删除 Project
	err = tx.Project.DeleteOneID(projectID).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete project: %w", err)
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
	// 个人项目：固定归属当前用户
	ownerUserID := &actorUserID
	if input.OwnerOrgID != nil {
		return CreateProjectInput{}, ErrForbidden // 不应通过此路径创建组织项目
	}
	return CreateProjectInput{
		Name:            name,
		OwnerUserID:     ownerUserID,
		Config:          cloneMap(input.Config),
		GlossaryEnabled: input.GlossaryEnabled,
		SourceLang:      normalizeLangOrDefault(input.SourceLang, "auto"),
		TargetLang:      normalizeLangOrDefault(input.TargetLang, "zh"),
	}, nil
}

func (s *ProjectService) normalizeUpdateInput(current *ent.Project, input UpdateProjectInput) (UpdateProjectInput, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = current.Name
	}
	configValue := cloneMap(input.Config)
	if len(configValue) == 0 {
		configValue = cloneMap(current.Config)
	}
	glossaryEnabled := input.GlossaryEnabled
	if glossaryEnabled == nil {
		currentVal := current.GlossaryEnabled
		glossaryEnabled = &currentVal
	}
	return UpdateProjectInput{
		Name:            name,
		Config:          configValue,
		GlossaryEnabled: glossaryEnabled,
		SourceLang:      normalizeLangOrDefault(input.SourceLang, current.SourceLang),
		TargetLang:      normalizeLangOrDefault(input.TargetLang, current.TargetLang),
	}, nil
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
