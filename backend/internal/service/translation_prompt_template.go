package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationprompttemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrTranslationPromptTemplateNotFound     = errors.New("translation prompt template not found")
	ErrTranslationPromptTemplateScopeInvalid = errors.New("translation prompt template scope invalid")
	ErrTranslationPromptTemplateInUse        = errors.New("translation prompt template is referenced by execution plan(s)")
)

// TranslationPromptTemplateService 提供翻译提示词模板的 CRUD 操作。
type TranslationPromptTemplateService struct {
	client *ent.Client
}

// NewTranslationPromptTemplateService 创建 TranslationPromptTemplateService 实例。
func NewTranslationPromptTemplateService(client *ent.Client) *TranslationPromptTemplateService {
	return &TranslationPromptTemplateService{client: client}
}

// CreateTranslationPromptTemplateInput 创建翻译提示词模板的输入参数。
type CreateTranslationPromptTemplateInput struct {
	Name                string
	Description         string
	Scope               string // user / org
	OwnerUserID         *int
	OwnerOrgID          *int
	SystemPromptContent string
}

// UpdateTranslationPromptTemplateInput 更新翻译提示词模板的输入参数。
type UpdateTranslationPromptTemplateInput struct {
	Name                *string
	Description         *string
	SystemPromptContent *string
}

// ListByUser 列出指定用户的所有翻译提示词模板（包含内置模板）。
func (s *TranslationPromptTemplateService) ListByUser(ctx context.Context, userID int) ([]*ent.TranslationPromptTemplate, error) {
	dbTemplates, err := s.client.TranslationPromptTemplate.Query().
		Where(
			translationprompttemplate.ScopeEQ("user"),
			translationprompttemplate.OwnerUserIDEQ(userID),
		).
		Order(ent.Asc(translationprompttemplate.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list translation prompt templates: %w", err)
	}
	return append(templates.BuiltinTranslationPromptTemplates(), dbTemplates...), nil
}

// ListByOrg 列出指定组织的所有翻译提示词模板（包含内置模板）。
func (s *TranslationPromptTemplateService) ListByOrg(ctx context.Context, orgID int) ([]*ent.TranslationPromptTemplate, error) {
	dbTemplates, err := s.client.TranslationPromptTemplate.Query().
		Where(
			translationprompttemplate.ScopeEQ("org"),
			translationprompttemplate.OwnerOrgIDEQ(orgID),
		).
		Order(ent.Asc(translationprompttemplate.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list translation prompt templates: %w", err)
	}
	return append(templates.BuiltinTranslationPromptTemplates(), dbTemplates...), nil
}

// GetByID 根据 ID 获取翻译提示词模板（支持内置模板）。
func (s *TranslationPromptTemplateService) GetByID(ctx context.Context, id int) (*ent.TranslationPromptTemplate, error) {
	if templates.IsBuiltinID(id) {
		pt := templates.BuiltinTranslationPromptTemplate(id)
		if pt == nil {
			return nil, ErrTranslationPromptTemplateNotFound
		}
		return pt, nil
	}
	pt, err := s.client.TranslationPromptTemplate.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTranslationPromptTemplateNotFound
		}
		return nil, fmt.Errorf("query translation prompt template: %w", err)
	}
	return pt, nil
}

// Create 创建翻译提示词模板。
func (s *TranslationPromptTemplateService) Create(ctx context.Context, input CreateTranslationPromptTemplateInput) (*ent.TranslationPromptTemplate, error) {
	if input.Scope == "" {
		input.Scope = "user"
	}
	if input.Scope != "user" && input.Scope != "org" && input.Scope != "system" {
		return nil, ErrTranslationPromptTemplateScopeInvalid
	}

	create := s.client.TranslationPromptTemplate.Create().
		SetName(input.Name).
		SetDescription(input.Description).
		SetScope(input.Scope).
		SetSystemPromptContent(input.SystemPromptContent)

	if input.OwnerUserID != nil {
		create.SetOwnerUserID(*input.OwnerUserID)
	}
	if input.OwnerOrgID != nil {
		create.SetOwnerOrgID(*input.OwnerOrgID)
	}

	pt, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create translation prompt template: %w", err)
	}
	return pt, nil
}

// Update 更新翻译提示词模板（内置模板不可修改）。
func (s *TranslationPromptTemplateService) Update(ctx context.Context, id int, input UpdateTranslationPromptTemplateInput) (*ent.TranslationPromptTemplate, error) {
	if templates.IsBuiltinID(id) {
		return nil, ErrTranslationPromptTemplateNotFound
	}
	pt, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if pt.Scope == "system" {
		return nil, ErrTranslationPromptTemplateNotFound // 系统模板不可修改
	}

	update := s.client.TranslationPromptTemplate.UpdateOneID(id)

	if input.Name != nil {
		update.SetName(*input.Name)
	}
	if input.Description != nil {
		update.SetDescription(*input.Description)
	}
	if input.SystemPromptContent != nil {
		update.SetSystemPromptContent(*input.SystemPromptContent)
	}

	updated, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update translation prompt template: %w", err)
	}
	return updated, nil
}

// Delete 删除翻译提示词模板（内置模板不可删除）。
func (s *TranslationPromptTemplateService) Delete(ctx context.Context, id int) error {
	if templates.IsBuiltinID(id) {
		return ErrTranslationPromptTemplateNotFound
	}
	pt, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if pt.Scope == "system" {
		return ErrTranslationPromptTemplateNotFound // 系统模板不可删除
	}

	// 检查是否有执行计划模板引用了该提示词模板（通过 translate 轮次）
	plans, err := s.client.ExecutionPlanTemplate.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("check execution plan references: %w", err)
	}
	for _, plan := range plans {
		for _, round := range plan.Rounds {
			if round.Mode == "translate" && round.Translate != nil && round.Translate.PromptTemplateID == id {
				return fmt.Errorf("%w: %q is referenced by execution plan %q",
					ErrTranslationPromptTemplateInUse, pt.Name, plan.Name)
			}
		}
	}

	return s.client.TranslationPromptTemplate.DeleteOneID(id).Exec(ctx)
}
