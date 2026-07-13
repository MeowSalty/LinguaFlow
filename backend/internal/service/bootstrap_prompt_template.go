package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bootstrapprompttemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrBootstrapPromptTemplateNotFound     = errors.New("bootstrap prompt template not found")
	ErrBootstrapPromptTemplateScopeInvalid = errors.New("bootstrap prompt template scope invalid")
	ErrBootstrapPromptTemplateInUse        = errors.New("bootstrap prompt template is referenced by execution plan(s)")
)

// BootstrapPromptTemplateService 提供术语抽取提示词模板的 CRUD 操作。
type BootstrapPromptTemplateService struct {
	client *ent.Client
}

// NewBootstrapPromptTemplateService 创建 BootstrapPromptTemplateService 实例。
func NewBootstrapPromptTemplateService(client *ent.Client) *BootstrapPromptTemplateService {
	return &BootstrapPromptTemplateService{client: client}
}

// CreateBootstrapPromptTemplateInput 创建术语抽取提示词模板的输入参数。
type CreateBootstrapPromptTemplateInput struct {
	Name        string
	Description string
	Scope       string // user / org
	OwnerUserID *int
	OwnerOrgID  *int
	Content     string
}

// UpdateBootstrapPromptTemplateInput 更新术语抽取提示词模板的输入参数。
type UpdateBootstrapPromptTemplateInput struct {
	Name        *string
	Description *string
	Content     *string
}

// ListByUser 列出指定用户的所有术语抽取提示词模板（包含内置模板）。
func (s *BootstrapPromptTemplateService) ListByUser(ctx context.Context, userID int) ([]*ent.BootstrapPromptTemplate, error) {
	dbTemplates, err := s.client.BootstrapPromptTemplate.Query().
		Where(
			bootstrapprompttemplate.ScopeEQ("user"),
			bootstrapprompttemplate.OwnerUserIDEQ(userID),
		).
		Order(ent.Asc(bootstrapprompttemplate.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list bootstrap prompt templates: %w", err)
	}
	return append(templates.BuiltinBootstrapPromptTemplates(), dbTemplates...), nil
}

// ListByOrg 列出指定组织的所有术语抽取提示词模板（包含内置模板）。
func (s *BootstrapPromptTemplateService) ListByOrg(ctx context.Context, orgID int) ([]*ent.BootstrapPromptTemplate, error) {
	dbTemplates, err := s.client.BootstrapPromptTemplate.Query().
		Where(
			bootstrapprompttemplate.ScopeEQ("org"),
			bootstrapprompttemplate.OwnerOrgIDEQ(orgID),
		).
		Order(ent.Asc(bootstrapprompttemplate.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list bootstrap prompt templates: %w", err)
	}
	return append(templates.BuiltinBootstrapPromptTemplates(), dbTemplates...), nil
}

// GetByID 根据 ID 获取术语抽取提示词模板（支持内置模板）。
func (s *BootstrapPromptTemplateService) GetByID(ctx context.Context, id int) (*ent.BootstrapPromptTemplate, error) {
	if templates.IsBuiltinID(id) {
		pt := templates.BuiltinBootstrapPromptTemplate(id)
		if pt == nil {
			return nil, ErrBootstrapPromptTemplateNotFound
		}
		return pt, nil
	}
	pt, err := s.client.BootstrapPromptTemplate.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrBootstrapPromptTemplateNotFound
		}
		return nil, fmt.Errorf("query bootstrap prompt template: %w", err)
	}
	return pt, nil
}

// Create 创建术语抽取提示词模板。
func (s *BootstrapPromptTemplateService) Create(ctx context.Context, input CreateBootstrapPromptTemplateInput) (*ent.BootstrapPromptTemplate, error) {
	if input.Scope == "" {
		input.Scope = "user"
	}
	if input.Scope != "user" && input.Scope != "org" && input.Scope != "system" {
		return nil, ErrBootstrapPromptTemplateScopeInvalid
	}

	create := s.client.BootstrapPromptTemplate.Create().
		SetName(input.Name).
		SetDescription(input.Description).
		SetScope(input.Scope).
		SetContent(input.Content)

	if input.OwnerUserID != nil {
		create.SetOwnerUserID(*input.OwnerUserID)
	}
	if input.OwnerOrgID != nil {
		create.SetOwnerOrgID(*input.OwnerOrgID)
	}

	pt, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create bootstrap prompt template: %w", err)
	}
	return pt, nil
}

// Update 更新术语抽取提示词模板（内置模板不可修改）。
func (s *BootstrapPromptTemplateService) Update(ctx context.Context, id int, input UpdateBootstrapPromptTemplateInput) (*ent.BootstrapPromptTemplate, error) {
	if templates.IsBuiltinID(id) {
		return nil, ErrBootstrapPromptTemplateNotFound
	}
	pt, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if pt.Scope == "system" {
		return nil, ErrBootstrapPromptTemplateNotFound // 系统模板不可修改
	}

	update := s.client.BootstrapPromptTemplate.UpdateOneID(id)

	if input.Name != nil {
		update.SetName(*input.Name)
	}
	if input.Description != nil {
		update.SetDescription(*input.Description)
	}
	if input.Content != nil {
		update.SetContent(*input.Content)
	}

	updated, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update bootstrap prompt template: %w", err)
	}
	return updated, nil
}

// Delete 删除术语抽取提示词模板（内置模板不可删除）。
func (s *BootstrapPromptTemplateService) Delete(ctx context.Context, id int) error {
	if templates.IsBuiltinID(id) {
		return ErrBootstrapPromptTemplateNotFound
	}
	pt, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if pt.Scope == "system" {
		return ErrBootstrapPromptTemplateNotFound // 系统模板不可删除
	}

	// 检查是否有执行计划模板引用了该提示词模板
	plans, err := s.client.ExecutionPlanTemplate.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("check execution plan references: %w", err)
	}
	for _, plan := range plans {
		if plan.Bootstrap.PromptTemplateID == id {
			return fmt.Errorf("%w: %q is referenced by execution plan %q",
				ErrBootstrapPromptTemplateInUse, pt.Name, plan.Name)
		}
	}

	return s.client.BootstrapPromptTemplate.DeleteOneID(id).Exec(ctx)
}
