package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationtemplate"
)

var (
	ErrTemplateNotFound        = errors.New("template not found")
	ErrTemplateBuiltinReadonly = errors.New("builtin template is readonly")
	ErrTemplateScopeInvalid    = errors.New("template scope invalid")
)

// TemplateService 提供翻译模板的 CRUD 操作。
type TemplateService struct {
	client *ent.Client
}

// NewTemplateService 创建 TemplateService 实例。
func NewTemplateService(client *ent.Client) *TemplateService {
	return &TemplateService{client: client}
}

// CreateTemplateInput 创建模板的输入参数。
type CreateTemplateInput struct {
	Name                string
	Description         string
	Scope               string // user / org
	OwnerUserID         *int
	OwnerOrgID          *int
	SystemPromptContent string
	PipelineConfig      *schema.TemplatePipelineConfigData
	GlossaryConfig      *schema.TemplateGlossaryConfigData
}

// UpdateTemplateInput 更新模板的输入参数。
type UpdateTemplateInput struct {
	Name                *string
	Description         *string
	SystemPromptContent *string
	PipelineConfig      *schema.TemplatePipelineConfigData
	GlossaryConfig      *schema.TemplateGlossaryConfigData
}

// ListByUser 列出指定用户的所有用户模板。
func (s *TemplateService) ListByUser(ctx context.Context, userID int) ([]*ent.TranslationTemplate, error) {
	return s.client.TranslationTemplate.Query().
		Where(
			translationtemplate.ScopeEQ("user"),
			translationtemplate.OwnerUserIDEQ(userID),
		).
		Order(ent.Asc(translationtemplate.FieldID)).
		All(ctx)
}

// ListByOrg 列出指定组织的所有组织模板。
func (s *TemplateService) ListByOrg(ctx context.Context, orgID int) ([]*ent.TranslationTemplate, error) {
	return s.client.TranslationTemplate.Query().
		Where(
			translationtemplate.ScopeEQ("org"),
			translationtemplate.OwnerOrgIDEQ(orgID),
		).
		Order(ent.Asc(translationtemplate.FieldID)).
		All(ctx)
}

// GetByID 根据 ID 获取模板。
func (s *TemplateService) GetByID(ctx context.Context, id int) (*ent.TranslationTemplate, error) {
	tmpl, err := s.client.TranslationTemplate.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("query template: %w", err)
	}
	return tmpl, nil
}

// Create 创建用户模板。
func (s *TemplateService) Create(ctx context.Context, input CreateTemplateInput) (*ent.TranslationTemplate, error) {
	if input.Scope == "" {
		input.Scope = "user"
	}
	if input.Scope != "user" && input.Scope != "org" {
		return nil, ErrTemplateScopeInvalid
	}

	create := s.client.TranslationTemplate.Create().
		SetName(input.Name).
		SetDescription(input.Description).
		SetScope(input.Scope)

	if input.OwnerUserID != nil {
		create.SetOwnerUserID(*input.OwnerUserID)
	}
	if input.OwnerOrgID != nil {
		create.SetOwnerOrgID(*input.OwnerOrgID)
	}
	if input.SystemPromptContent != "" {
		create.SetSystemPromptContent(input.SystemPromptContent)
	}
	if input.PipelineConfig != nil {
		create.SetPipelineConfig(*input.PipelineConfig)
	}
	if input.GlossaryConfig != nil {
		create.SetGlossaryConfig(*input.GlossaryConfig)
	}

	tmpl, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create template: %w", err)
	}
	return tmpl, nil
}

// Update 更新用户模板。仅允许更新用户自己的模板。
func (s *TemplateService) Update(ctx context.Context, id int, input UpdateTemplateInput) (*ent.TranslationTemplate, error) {
	tmpl, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if tmpl.Scope != "user" && tmpl.Scope != "org" {
		return nil, ErrTemplateBuiltinReadonly
	}

	update := s.client.TranslationTemplate.UpdateOneID(id)

	if input.Name != nil {
		update.SetName(*input.Name)
	}
	if input.Description != nil {
		update.SetDescription(*input.Description)
	}
	if input.SystemPromptContent != nil {
		update.SetSystemPromptContent(*input.SystemPromptContent)
	}
	if input.PipelineConfig != nil {
		update.SetPipelineConfig(*input.PipelineConfig)
	}
	if input.GlossaryConfig != nil {
		update.SetGlossaryConfig(*input.GlossaryConfig)
	}

	updated, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update template: %w", err)
	}
	return updated, nil
}

// Delete 删除用户模板。仅允许删除用户自己的模板。
func (s *TemplateService) Delete(ctx context.Context, id int) error {
	tmpl, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if tmpl.Scope != "user" && tmpl.Scope != "org" {
		return ErrTemplateBuiltinReadonly
	}
	return s.client.TranslationTemplate.DeleteOneID(id).Exec(ctx)
}

// CopyToUser 将模板（内置或用户）复制为用户模板。
func (s *TemplateService) CopyToUser(ctx context.Context, sourceID int, userID int, newName string) (*ent.TranslationTemplate, error) {
	var source *ent.TranslationTemplate

	builtin := FindBuiltinTemplate(sourceID)
	if builtin != nil {
		// 从内置模板创建
		data := BuiltinTemplateToTemplateData(builtin)
		source = data.Template
	} else {
		var err error
		source, err = s.GetByID(ctx, sourceID)
		if err != nil {
			return nil, err
		}
	}

	if newName == "" {
		newName = source.Name + "(副本)"
	}

	return s.client.TranslationTemplate.Create().
		SetName(newName).
		SetDescription(source.Description).
		SetScope("user").
		SetOwnerUserID(userID).
		SetSystemPromptContent(source.SystemPromptContent).
		SetPipelineConfig(source.PipelineConfig).
		SetGlossaryConfig(source.GlossaryConfig).
		Save(ctx)
}
