package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationprofile"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrTranslationProfileNotFound      = errors.New("translation profile not found")
	ErrTranslationProfileScopeInvalid  = errors.New("translation profile scope invalid")
	ErrTranslationProfileConfigInvalid = errors.New("translation profile config invalid")
)

// TranslationProfileService 提供翻译配置的 CRUD 操作。
type TranslationProfileService struct {
	client *ent.Client
}

// NewTranslationProfileService 创建 TranslationProfileService 实例。
func NewTranslationProfileService(client *ent.Client) *TranslationProfileService {
	return &TranslationProfileService{client: client}
}

// CreateTranslationProfileInput 创建翻译配置的输入参数。
type CreateTranslationProfileInput struct {
	Name        string
	Description string
	Scope       string // user / org
	OwnerUserID *int
	OwnerOrgID  *int
	Config      *schema.TranslationProfileConfigData
}

// UpdateTranslationProfileInput 更新翻译配置的输入参数。
type UpdateTranslationProfileInput struct {
	Name        *string
	Description *string
	Config      *schema.TranslationProfileConfigData
}

// ListByUser 列出指定用户的所有翻译配置（包含内置策略）。
func (s *TranslationProfileService) ListByUser(ctx context.Context, userID int) ([]*ent.TranslationProfile, error) {
	dbProfiles, err := s.client.TranslationProfile.Query().
		Where(
			translationprofile.ScopeEQ("user"),
			translationprofile.OwnerUserIDEQ(userID),
		).
		Order(ent.Asc(translationprofile.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list translation profiles: %w", err)
	}
	profiles := append(templates.BuiltinTranslationProfiles(), dbProfiles...)
	for _, p := range profiles {
		p.Config.NormalizePreserveKinds()
	}
	return profiles, nil
}

// ListByOrg 列出指定组织的所有翻译配置（包含内置策略）。
func (s *TranslationProfileService) ListByOrg(ctx context.Context, orgID int) ([]*ent.TranslationProfile, error) {
	dbProfiles, err := s.client.TranslationProfile.Query().
		Where(
			translationprofile.ScopeEQ("org"),
			translationprofile.OwnerOrgIDEQ(orgID),
		).
		Order(ent.Asc(translationprofile.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list translation profiles: %w", err)
	}
	profiles := append(templates.BuiltinTranslationProfiles(), dbProfiles...)
	for _, p := range profiles {
		p.Config.NormalizePreserveKinds()
	}
	return profiles, nil
}

// GetByID 根据 ID 获取翻译配置（支持内置策略）。
func (s *TranslationProfileService) GetByID(ctx context.Context, id int) (*ent.TranslationProfile, error) {
	if templates.IsBuiltinID(id) {
		tp := templates.BuiltinTranslationProfile(id)
		if tp == nil {
			return nil, ErrTranslationProfileNotFound
		}
		tp.Config.NormalizePreserveKinds()
		return tp, nil
	}
	tp, err := s.client.TranslationProfile.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTranslationProfileNotFound
		}
		return nil, fmt.Errorf("query translation profile: %w", err)
	}
	tp.Config.NormalizePreserveKinds()
	return tp, nil
}

// Create 创建翻译配置。
func (s *TranslationProfileService) Create(ctx context.Context, input CreateTranslationProfileInput) (*ent.TranslationProfile, error) {
	if input.Scope == "" {
		input.Scope = "user"
	}
	if input.Scope != "user" && input.Scope != "org" && input.Scope != "system" {
		return nil, ErrTranslationProfileScopeInvalid
	}

	// 校验 Config
	if input.Config != nil {
		if err := validateProfileConfig(input.Config); err != nil {
			return nil, err
		}
	}

	create := s.client.TranslationProfile.Create().
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
		return nil, fmt.Errorf("create translation profile: %w", err)
	}
	return tp, nil
}

// Update 更新翻译配置（内置策略不可修改）。
func (s *TranslationProfileService) Update(ctx context.Context, id int, input UpdateTranslationProfileInput) (*ent.TranslationProfile, error) {
	if templates.IsBuiltinID(id) {
		return nil, ErrTranslationProfileNotFound
	}
	tp, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if tp.Scope == "system" {
		return nil, ErrTranslationProfileNotFound // 系统配置不可修改
	}

	// 校验 Config
	if input.Config != nil {
		if err := validateProfileConfig(input.Config); err != nil {
			return nil, err
		}
	}

	update := s.client.TranslationProfile.UpdateOneID(id)

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
		return nil, fmt.Errorf("update translation profile: %w", err)
	}
	return updated, nil
}

// Delete 删除翻译配置（内置策略不可删除）。
func (s *TranslationProfileService) Delete(ctx context.Context, id int) error {
	if templates.IsBuiltinID(id) {
		return ErrTranslationProfileNotFound
	}
	tp, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if tp.Scope == "system" {
		return ErrTranslationProfileNotFound // 系统配置不可删除
	}
	return s.client.TranslationProfile.DeleteOneID(id).Exec(ctx)
}

// validateProfileConfig 校验翻译配置的有效性。
func validateProfileConfig(cfg *schema.TranslationProfileConfigData) error {
	if cfg.Split.Enabled {
		if cfg.Split.MaxChars < 1 {
			return fmt.Errorf("%w: split.max_chars must be >= 1 when split is enabled", ErrTranslationProfileConfigInvalid)
		}
	}
	validRubyKinds := map[string]bool{"phonetic": true, "semantic": true, "creative": true}
	for _, k := range cfg.Ruby.PreserveKinds {
		if !validRubyKinds[k] {
			return fmt.Errorf("%w: ruby.preserve_kinds contains invalid kind %q (must be one of phonetic, semantic, creative)", ErrTranslationProfileConfigInvalid, k)
		}
	}
	return nil
}
