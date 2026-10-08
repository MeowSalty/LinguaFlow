package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
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
	httpClients telemetry.HTTPClientFactory
	policyMu    sync.Mutex // serialize committed configuration changes with the in-process registry
	credentials *CredentialService
}

type BackendInput struct {
	Name               string
	Type               string
	Options            map[string]any
	RateLimitPerMinute int
	Secret             *string
	CredentialID       *int
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
	Credential         credential.Binding
	HasSecret          bool
}

func NewBackendService(client *ent.Client, users *UserService, limiterPool *backend.LimiterPool, clients ...telemetry.HTTPClientFactory) *BackendService {
	s := &BackendService{client: client, users: users, limiterPool: limiterPool}
	if len(clients) > 0 {
		s.httpClients = clients[0]
	}
	return s
}

// SetCredentials wires the shared runtime repository before handlers/workers start.
func (s *BackendService) SetCredentials(c *CredentialService) { s.credentials = c }
func (s *BackendService) Credentials() *CredentialService     { return s.credentials }

// Create 创建后端。
// scope 由调用方传入（handler 从认证上下文推断）。
// 权限校验由 handler 层负责：
//   - user scope：handler 直接从认证上下文取 actorUserID 作为 OwnerUserID（天然隔离）
//   - org scope：handler 必须先验证 actorUserID 是 orgID 的管理员（RequireMembership）
func (s *BackendService) Create(ctx context.Context, input CreateBackendInput) (*BackendRecord, error) {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	normalized, err := normalizeBackendInput(input.BackendInput)
	if err != nil {
		return nil, err
	}
	if s.credentials == nil {
		return nil, credential.ErrUnavailable
	}
	s.credentials.mu.Lock()
	defer s.credentials.mu.Unlock()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ownerID := 0
	if input.Scope == ScopeUser && input.OwnerUserID != nil {
		ownerID = *input.OwnerUserID
	}
	if input.Scope == ScopeOrg && input.OwnerOrgID != nil {
		ownerID = *input.OwnerOrgID
	}
	credRow, err := s.bindCredential(ctx, tx.Client(), normalized, input.Scope, ownerID, nil)
	if err != nil {
		return nil, err
	}
	create := tx.Backend.Create().
		SetName(normalized.Name).
		SetBackendType(entbackend.BackendType(normalized.Type)).
		SetOptions(cloneMap(normalized.Options)).
		SetRateLimitPerMinute(normalized.RateLimitPerMinute).
		SetScope(input.Scope).
		SetCredentialID(credRow.ID)

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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	created.Edges.Credential = credRow
	if s.limiterPool != nil {
		s.limiterPool.Register(created.ID, created.RateLimitPerMinute)
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
	rows, err := query.WithCredential().
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
	row, err := s.client.Backend.Query().Where(entbackend.IDEQ(backendID)).WithCredential().Only(ctx)
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
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	previous, err := s.requireOwnership(ctx, actorUserID, backendID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeBackendInput(input)
	if err != nil {
		return nil, err
	}
	if s.credentials == nil {
		return nil, credential.ErrUnavailable
	}
	s.credentials.mu.Lock()
	defer s.credentials.mu.Unlock()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ownerID := 0
	if previous.OwnerUserID != nil {
		ownerID = *previous.OwnerUserID
	}
	if previous.OwnerOrgID != nil {
		ownerID = *previous.OwnerOrgID
	}
	credRow, err := s.bindCredential(ctx, tx.Client(), normalized, previous.Scope, ownerID, previous.CredentialID)
	if err != nil {
		return nil, err
	}
	updated, err := tx.Backend.UpdateOneID(backendID).
		SetName(normalized.Name).
		SetBackendType(entbackend.BackendType(normalized.Type)).
		SetOptions(cloneMap(normalized.Options)).
		SetCredentialID(credRow.ID).
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	updated.Edges.Credential = credRow
	if s.limiterPool != nil {
		s.limiterPool.Refresh(backendID, updated.RateLimitPerMinute)
	}
	return backendRecord(updated), nil
}

// Delete 删除后端。需要 actorUserID 验证权限。
func (s *BackendService) Delete(ctx context.Context, actorUserID, backendID int) error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
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
			rows, err = s.client.Backend.Query().WithCredential().
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
			rows, err = s.client.Backend.Query().WithCredential().
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
		rows, err := s.client.Backend.Query().WithCredential().
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
	r := &BackendRecord{
		ID:                 row.ID,
		Scope:              row.Scope,
		Name:               row.Name,
		Type:               string(row.BackendType),
		Options:            publicBackendOptions(row.Options),
		RateLimitPerMinute: row.RateLimitPerMinute,
		OwnerUserID:        row.OwnerUserID,
		OwnerOrgID:         row.OwnerOrgID,
	}
	if c := row.Edges.Credential; c != nil {
		r.Credential = credential.Binding{ID: c.ID, Version: c.CurrentVersion}
		r.HasSecret = true
	}
	return r
}

// ListModels 凭 api_key(+base_url) 探测上游可用模型列表，不落库。
func (s *BackendService) ListModels(ctx context.Context, typ string, opts map[string]any) ([]backend.ModelInfo, error) {
	typ = strings.ToLower(strings.TrimSpace(typ))
	if !isAllowedBackendType(typ) {
		return nil, ErrBackendTypeInvalid
	}
	secret, _ := opts["api_key"].(string)
	endpoint, _ := opts["base_url"].(string)
	registry := credential.NewMemory()
	defer registry.Close()
	binding, err := registry.Register(typ, endpoint, secret)
	if err != nil {
		return nil, err
	}
	client, err := credential.GuardClient(telemetry.ClientFor(s.httpClients, typ, "list_models"), registry, binding, 0, typ, endpoint)
	if err != nil {
		return nil, err
	}
	runtimeOptions := cloneMap(opts)
	runtimeOptions["base_url"], err = credential.NormalizeEndpoint(typ, endpoint)
	if err != nil {
		return nil, err
	}
	lister, err := backend.NewModelLister(typ, runtimeOptions, client)
	if err != nil {
		return nil, err
	}
	return lister.ListModels(ctx)
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
	opts := cloneMap(input.Options)
	if _, ok := opts["api_key"]; ok {
		return BackendInput{}, fmt.Errorf("%w: options.api_key is not supported; use secret or credential_id", ErrInvalidInput)
	}
	for key, value := range opts {
		if !publicBackendOption(key) {
			return BackendInput{}, fmt.Errorf("%w: unsupported backend option %s", ErrInvalidInput, key)
		}
		if key == "enable_prompt_cache" && typ != BackendTypeAnthropic {
			return BackendInput{}, ErrInvalidInput
		}
		switch key {
		case "model", "type", "base_url", "response_format", "thinking_level":
			if _, ok := value.(string); !ok {
				return BackendInput{}, ErrInvalidInput
			}
		case "stream", "enable_prompt_cache":
			if _, ok := value.(bool); !ok {
				return BackendInput{}, ErrInvalidInput
			}
		default:
			var n float64
			switch v := value.(type) {
			case float64:
				n = v
			case int:
				n = float64(v)
			case int64:
				n = float64(v)
			default:
				return BackendInput{}, ErrInvalidInput
			}
			if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n >= float64(math.MaxInt64) {
				return BackendInput{}, ErrInvalidInput
			}
			if (key == "timeout" || key == "max_tokens") && math.Trunc(n) != n {
				return BackendInput{}, ErrInvalidInput
			}
			if key == "temperature" && n > 2 || key == "top_p" && n > 1 {
				return BackendInput{}, ErrInvalidInput
			}
			if typ == BackendTypeAnthropic && key == "temperature" && n > 1 {
				return BackendInput{}, ErrInvalidInput
			}
			if typ != BackendTypeOpenAI && key == "max_tokens" && n == 0 {
				return BackendInput{}, ErrInvalidInput
			}
		}
	}
	model, _ := opts["model"].(string)
	if strings.TrimSpace(model) == "" {
		return BackendInput{}, ErrInvalidInput
	}
	opts["model"] = strings.TrimSpace(model)
	if format, ok := opts["response_format"].(string); ok {
		switch format {
		case "json_schema", "json_object", "text", "none":
		default:
			return BackendInput{}, ErrInvalidInput
		}
	}
	if _, err := backend.ParseThinking(opts); err != nil {
		return BackendInput{}, ErrInvalidInput
	}
	if input.RateLimitPerMinute < 0 {
		return BackendInput{}, ErrInvalidInput
	}
	if input.Secret != nil && (*input.Secret == "" || input.CredentialID != nil) {
		return BackendInput{}, ErrInvalidInput
	}
	if input.CredentialID != nil && *input.CredentialID <= 0 {
		return BackendInput{}, ErrInvalidInput
	}
	if t, ok := opts["type"].(string); ok && t != typ {
		return BackendInput{}, ErrInvalidInput
	}
	ep, _ := opts["base_url"].(string)
	endpoint, err := credential.NormalizeEndpoint(typ, ep)
	if err != nil {
		return BackendInput{}, err
	}
	opts["base_url"] = endpoint
	return BackendInput{
		Name:               name,
		Type:               typ,
		Options:            opts,
		RateLimitPerMinute: input.RateLimitPerMinute,
		Secret:             input.Secret, CredentialID: input.CredentialID,
	}, nil
}

func publicBackendOption(key string) bool {
	switch key {
	case "type", "model", "base_url", "max_tokens", "timeout", "temperature", "top_p", "response_format", "stream", "thinking_level", "enable_prompt_cache":
		return true
	}
	return false
}
func publicBackendOptions(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if publicBackendOption(k) {
			out[k] = v
		}
	}
	return out
}
func (s *BackendService) bindCredential(ctx context.Context, client *ent.Client, input BackendInput, scope string, ownerID int, previous *int) (*ent.Credential, error) {
	if ownerID <= 0 {
		return nil, ErrInvalidInput
	}
	ep, _ := input.Options["base_url"].(string)
	if input.Secret != nil {
		return s.credentials.createWith(ctx, client, CreateCredentialInput{Scope: scope, OwnerID: ownerID, Provider: input.Type, Endpoint: ep, Secret: *input.Secret})
	}
	id := input.CredentialID
	if id == nil {
		id = previous
	}
	if id == nil {
		return nil, credential.ErrUnavailable
	}
	row, err := client.Credential.Get(ctx, *id)
	if ent.IsNotFound(err) {
		return nil, credential.ErrUnavailable
	}
	if err != nil {
		return nil, err
	}
	if row.Scope != scope || row.OwnerID != ownerID {
		return nil, credential.ErrOwnership
	}
	if row.Provider != input.Type || row.Endpoint != ep {
		return nil, credential.ErrEndpoint
	}
	v, err := s.credentials.version(ctx, client, credential.Binding{ID: row.ID, Version: row.CurrentVersion})
	if err != nil {
		return nil, err
	}
	if v.Revoked {
		return nil, credential.ErrRevoked
	}
	return row, nil
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
