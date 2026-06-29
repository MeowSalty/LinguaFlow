package service

import (
	"context"
	"errors"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

const (
	BackendTypeOpenAI    = "openai"
	BackendTypeAnthropic = "anthropic"
	BackendTypeGoogle    = "google"

	ScopeUser = "user"
	ScopeOrg  = "org"
)

var (
	ErrBackendNotFound      = errors.New("backend not found")
	ErrBackendExists        = errors.New("backend already exists")
	ErrBackendTypeInvalid   = errors.New("backend type invalid")
	ErrBackendSourceInvalid = errors.New("backend source invalid")
	ErrBackendNameAmbiguous = errors.New("backend name ambiguous")
	ErrProjectOwnerMissing  = errors.New("project owner missing")
)

type BackendService struct {
	client      *ent.Client
	users       *UserService
	limiterPool *backend.LimiterPool
}

type BackendInput struct {
	Name               string
	Type               string
	Options            map[string]any
	RateLimitPerMinute int
}

type CreateBackendInput struct {
	Scope string
	BackendInput
	OwnerUserID *int
	OwnerOrgID  *int
}

type BackendRecord struct {
	ID                 int
	Scope              string
	Name               string
	Type               string
	Options            map[string]any
	RateLimitPerMinute int
	OwnerUserID        *int
	OwnerOrgID         *int
}

func NewBackendService(client *ent.Client, users *UserService, limiterPool *backend.LimiterPool) *BackendService {
	return &BackendService{client: client, users: users, limiterPool: limiterPool}
}

// Create 创建后端。
// scope 由调用方传入（handler 从认证上下文推断）。
// 权限校验由 handler 层负责：
//   - user scope：handler 直接从认证上下文取 actorUserID 作为 OwnerUserID（天然隔离）
//   - org scope：handler 必须先验证 actorUserID 是 orgID 的管理员（RequireMembership）
func (s *BackendService) Create(ctx context.Context, input CreateBackendInput) (*BackendRecord, error) {
	normalized, err := normalizeBackendInput(input.BackendInput)
	if err != nil {
		return nil, err
	}
	create := s.client.Backend.Create().
		SetName(normalized.Name).
		SetBackendType(entbackend.BackendType(normalized.Type)).
		SetOptions(cloneMap(normalized.Options)).
		SetRateLimitPerMinute(normalized.RateLimitPerMinute).
		SetScope(input.Scope)

	switch input.Scope {
	case ScopeUser:
		if input.OwnerUserID == nil {
			return nil, ErrInvalidInput
		}
		create.SetOwnerUserID(*input.OwnerUserID)
	case ScopeOrg:
		if input.OwnerOrgID == nil {
			return nil, ErrInvalidInput
		}
		create.SetOwnerOrgID(*input.OwnerOrgID)
	default:
		return nil, ErrBackendSourceInvalid
	}

	created, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrBackendExists
		}
		return nil, err
	}
	return backendRecord(created), nil
}

// List 列出指定 scope 的后端。
func (s *BackendService) List(ctx context.Context, scope string, ownerID int) ([]*BackendRecord, error) {
	query := s.client.Backend.Query()
	switch scope {
	case ScopeUser:
		query = query.Where(
			entbackend.ScopeEQ(ScopeUser),
			entbackend.OwnerUserIDEQ(ownerID),
		)
	case ScopeOrg:
		query = query.Where(
			entbackend.ScopeEQ(ScopeOrg),
			entbackend.OwnerOrgIDEQ(ownerID),
		)
	default:
		return nil, ErrBackendSourceInvalid
	}
	rows, err := query.
		Order(ent.Asc(entbackend.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*BackendRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, backendRecord(row))
	}
	return out, nil
}

// GetByID 根据 ID 获取后端。
func (s *BackendService) GetByID(ctx context.Context, backendID int) (*BackendRecord, error) {
	row, err := s.client.Backend.Get(ctx, backendID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrBackendNotFound
		}
		return nil, err
	}
	return backendRecord(row), nil
}

// requireOwnership 验证 actorUserID 是否拥有指定后端。
// user scope：直接比对 owner_user_id；
// org scope：验证用户是否为该组织成员。
func (s *BackendService) requireOwnership(ctx context.Context, actorUserID, backendID int) (*ent.Backend, error) {
	row, err := s.client.Backend.Get(ctx, backendID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrBackendNotFound
		}
		return nil, err
	}
	switch row.Scope {
	case ScopeUser:
		if row.OwnerUserID == nil || *row.OwnerUserID != actorUserID {
			return nil, ErrBackendNotFound // 不泄露后端存在性
		}
	case ScopeOrg:
		if row.OwnerOrgID == nil {
			return nil, ErrBackendNotFound
		}
		if _, err := s.users.requireMembership(ctx, actorUserID, *row.OwnerOrgID, OrgRoleAdmin); err != nil {
			return nil, err
		}
	default:
		return nil, ErrBackendSourceInvalid
	}
	return row, nil
}

// Update 更新后端。需要 actorUserID 验证权限。
func (s *BackendService) Update(ctx context.Context, actorUserID, backendID int, input BackendInput) (*BackendRecord, error) {
	if _, err := s.requireOwnership(ctx, actorUserID, backendID); err != nil {
		return nil, err
	}
	normalized, err := normalizeBackendInput(input)
	if err != nil {
		return nil, err
	}
	updated, err := s.client.Backend.UpdateOneID(backendID).
		SetName(normalized.Name).
		SetBackendType(entbackend.BackendType(normalized.Type)).
		SetOptions(cloneMap(normalized.Options)).
		SetRateLimitPerMinute(normalized.RateLimitPerMinute).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrBackendNotFound
		}
		if ent.IsConstraintError(err) {
			return nil, ErrBackendExists
		}
		return nil, err
	}
	if s.limiterPool != nil {
		s.limiterPool.Refresh(backendID, updated.RateLimitPerMinute)
	}
	return backendRecord(updated), nil
}

// Delete 删除后端。需要 actorUserID 验证权限。
func (s *BackendService) Delete(ctx context.Context, actorUserID, backendID int) error {
	if _, err := s.requireOwnership(ctx, actorUserID, backendID); err != nil {
		return err
	}
	err := s.client.Backend.DeleteOneID(backendID).Exec(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrBackendNotFound
		}
		return err
	}
	if s.limiterPool != nil {
		s.limiterPool.Remove(backendID)
	}
	return nil
}

// resolveAccessibleBackends 查询项目可访问的所有后端。
// 单表查询，scope + owner 信息从记录读取。
func (s *BackendService) resolveAccessibleBackends(ctx context.Context, project *ent.Project) ([]*BackendRecord, error) {
	if project.OwnerUserID != nil {
		// 用户项目：可访问自己 + 所属组织的后端
		orgIDs, _ := s.client.Organization.Query().
			Where(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(*project.OwnerUserID)))).
			IDs(ctx)

		userPred := entbackend.And(
			entbackend.ScopeEQ(ScopeUser),
			entbackend.OwnerUserIDEQ(*project.OwnerUserID),
		)

		var rows []*ent.Backend
		var err error
		if len(orgIDs) > 0 {
			rows, err = s.client.Backend.Query().
				Where(entbackend.Or(
					userPred,
					entbackend.And(
						entbackend.ScopeEQ(ScopeOrg),
						entbackend.OwnerOrgIDIn(orgIDs...),
					),
				)).
				Order(ent.Asc(entbackend.FieldID)).
				All(ctx)
		} else {
			rows, err = s.client.Backend.Query().
				Where(userPred).
				Order(ent.Asc(entbackend.FieldID)).
				All(ctx)
		}
		if err != nil {
			return nil, err
		}
		out := make([]*BackendRecord, 0, len(rows))
		for _, row := range rows {
			out = append(out, backendRecord(row))
		}
		return out, nil
	}

	if project.OwnerOrgID != nil {
		// 组织项目：仅可访问该组织的后端
		rows, err := s.client.Backend.Query().
			Where(
				entbackend.ScopeEQ(ScopeOrg),
				entbackend.OwnerOrgIDEQ(*project.OwnerOrgID),
			).
			Order(ent.Asc(entbackend.FieldID)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]*BackendRecord, 0, len(rows))
		for _, row := range rows {
			out = append(out, backendRecord(row))
		}
		return out, nil
	}

	return nil, ErrProjectOwnerMissing
}

// backendRecord 统一转换函数（替代原 userBackendRecord / orgBackendRecord）。
func backendRecord(row *ent.Backend) *BackendRecord {
	return &BackendRecord{
		ID:                 row.ID,
		Scope:              row.Scope,
		Name:               row.Name,
		Type:               string(row.BackendType),
		Options:            cloneMap(row.Options),
		RateLimitPerMinute: row.RateLimitPerMinute,
		OwnerUserID:        row.OwnerUserID,
		OwnerOrgID:         row.OwnerOrgID,
	}
}

func normalizeBackendInput(input BackendInput) (BackendInput, error) {
	name := strings.TrimSpace(input.Name)
	typ := strings.ToLower(strings.TrimSpace(input.Type))
	if name == "" || typ == "" {
		return BackendInput{}, ErrInvalidInput
	}
	if !isAllowedBackendType(typ) {
		return BackendInput{}, ErrBackendTypeInvalid
	}
	if input.RateLimitPerMinute < 0 {
		input.RateLimitPerMinute = 0
	}
	return BackendInput{
		Name:               name,
		Type:               typ,
		Options:            cloneMap(input.Options),
		RateLimitPerMinute: input.RateLimitPerMinute,
	}, nil
}

func isAllowedBackendType(typ string) bool {
	switch typ {
	case BackendTypeOpenAI, BackendTypeAnthropic, BackendTypeGoogle:
		return true
	default:
		return false
	}
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
