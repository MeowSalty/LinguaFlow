package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/executionprofile"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrExecutionProfileNotFound      = errors.New("execution profile not found")
	ErrExecutionProfileScopeInvalid  = errors.New("execution profile scope invalid")
	ErrExecutionProfileConfigInvalid = errors.New("execution profile config invalid")
)

// ExecutionProfileService 提供执行策略配置的 CRUD 操作。
type ExecutionProfileService struct {
	client *ent.Client
}

// NewExecutionProfileService 创建 ExecutionProfileService 实例。
func NewExecutionProfileService(client *ent.Client) *ExecutionProfileService {
	return &ExecutionProfileService{client: client}
}

// CreateExecutionProfileInput 创建执行策略配置的输入参数。
type CreateExecutionProfileInput struct {
	Name        string
	Description string
	Scope       string // user / org
	OwnerUserID *int
	OwnerOrgID  *int
	Config      *schema.ExecutionProfileConfigData
}

// UpdateExecutionProfileInput 更新执行策略配置的输入参数。
type UpdateExecutionProfileInput struct {
	Name        *string
	Description *string
	Config      *schema.ExecutionProfileConfigData
}

// ListByUser 列出指定用户的所有执行策略配置（包含内置策略）。
func (s *ExecutionProfileService) ListByUser(ctx context.Context, userID int) ([]*ent.ExecutionProfile, error) {
	dbProfiles, err := s.client.ExecutionProfile.Query().
		Where(
			executionprofile.ScopeEQ("user"),
			executionprofile.OwnerUserIDEQ(userID),
		).
		Order(ent.Asc(executionprofile.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list execution profiles: %w", err)
	}
	profiles := append(templates.BuiltinExecutionProfiles(), dbProfiles...)
	for _, p := range profiles {
		p.Config.NormalizePreserveKinds()
	}
	return profiles, nil
}

// ListByOrg 列出指定组织的所有执行策略配置（包含内置策略）。
func (s *ExecutionProfileService) ListByOrg(ctx context.Context, orgID int) ([]*ent.ExecutionProfile, error) {
	dbProfiles, err := s.client.ExecutionProfile.Query().
		Where(
			executionprofile.ScopeEQ("org"),
			executionprofile.OwnerOrgIDEQ(orgID),
		).
		Order(ent.Asc(executionprofile.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list execution profiles: %w", err)
	}
	profiles := append(templates.BuiltinExecutionProfiles(), dbProfiles...)
	for _, p := range profiles {
		p.Config.NormalizePreserveKinds()
	}
	return profiles, nil
}

// GetByID 根据 ID 获取执行策略配置（支持内置策略）。
func (s *ExecutionProfileService) GetByID(ctx context.Context, id int) (*ent.ExecutionProfile, error) {
	if templates.IsBuiltinID(id) {
		tp := templates.BuiltinExecutionProfile(id)
		if tp == nil {
			return nil, ErrExecutionProfileNotFound
		}
		tp.Config.NormalizePreserveKinds()
		return tp, nil
	}
	tp, err := s.client.ExecutionProfile.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrExecutionProfileNotFound
		}
		return nil, fmt.Errorf("query execution profile: %w", err)
	}
	tp.Config.NormalizePreserveKinds()
	return tp, nil
}

// Create 创建执行策略配置。
func (s *ExecutionProfileService) Create(ctx context.Context, input CreateExecutionProfileInput) (*ent.ExecutionProfile, error) {
	if input.Scope == "" {
		input.Scope = "user"
	}
	if input.Scope != "user" && input.Scope != "org" && input.Scope != "system" {
		return nil, ErrExecutionProfileScopeInvalid
	}

	// 校验 Config
	if input.Config != nil {
		if err := validateProfileConfig(input.Config); err != nil {
			return nil, err
		}
	}

	create := s.client.ExecutionProfile.Create().
		SetName(input.Name).
		SetDescription(input.Description).
		SetScope(input.Scope)

	if input.OwnerUserID != nil {
		create.SetOwnerUserID(*input.OwnerUserID)
	}
	if input.OwnerOrgID != nil {
		create.SetOwnerOrgID(*input.OwnerOrgID)
	}
	if input.Config != nil {
		create.SetConfig(*input.Config)
	}

	tp, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create execution profile: %w", err)
	}
	return tp, nil
}

// Update 更新执行策略配置（内置策略不可修改）。
func (s *ExecutionProfileService) Update(ctx context.Context, id int, input UpdateExecutionProfileInput) (*ent.ExecutionProfile, error) {
	if templates.IsBuiltinID(id) {
		return nil, ErrExecutionProfileNotFound
	}
	tp, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if tp.Scope == "system" {
		return nil, ErrExecutionProfileNotFound // 系统配置不可修改
	}

	// 校验 Config
	if input.Config != nil {
		if err := validateProfileConfig(input.Config); err != nil {
			return nil, err
		}
	}

	update := s.client.ExecutionProfile.UpdateOneID(id)

	if input.Name != nil {
		update.SetName(*input.Name)
	}
	if input.Description != nil {
		update.SetDescription(*input.Description)
	}
	if input.Config != nil {
		update.SetConfig(*input.Config)
	}

	updated, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update execution profile: %w", err)
	}
	return updated, nil
}

// Delete 删除执行策略配置（内置策略不可删除）。
func (s *ExecutionProfileService) Delete(ctx context.Context, id int) error {
	if templates.IsBuiltinID(id) {
		return ErrExecutionProfileNotFound
	}
	tp, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if tp.Scope == "system" {
		return ErrExecutionProfileNotFound // 系统配置不可删除
	}
	return s.client.ExecutionProfile.DeleteOneID(id).Exec(ctx)
}

// validateProfileConfig 校验执行策略配置的有效性。
func validateProfileConfig(cfg *schema.ExecutionProfileConfigData) error {
	validRubyKinds := map[string]bool{"phonetic": true, "semantic": true, "creative": true}
	for _, k := range cfg.Ruby.PreserveKinds {
		if !validRubyKinds[k] {
			return fmt.Errorf("%w: ruby.preserve_kinds contains invalid kind %q (must be one of phonetic, semantic, creative)", ErrExecutionProfileConfigInvalid, k)
		}
	}

	if cfg.QA.Enabled {
		validLengthMethods := map[string]bool{"char_weight": true, "word_count": true, "": true}
		if !validLengthMethods[cfg.QA.LengthMethod] {
			return fmt.Errorf("%w: qa.length_method must be one of char_weight, word_count", ErrExecutionProfileConfigInvalid)
		}
		if cfg.QA.LengthRatioMin > 0 && cfg.QA.LengthRatioMax > 0 && cfg.QA.LengthRatioMin > cfg.QA.LengthRatioMax {
			return fmt.Errorf("%w: qa.length_ratio_min (%v) must not exceed qa.length_ratio_max (%v)", ErrExecutionProfileConfigInvalid, cfg.QA.LengthRatioMin, cfg.QA.LengthRatioMax)
		}
	}

	return nil
}
