package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/pruneprompttemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrPrunePromptTemplateNotFound     = errors.New("prune prompt template not found")
	ErrPrunePromptTemplateScopeInvalid = errors.New("prune prompt template scope invalid")
	ErrPrunePromptTemplateInUse        = errors.New("prune prompt template is referenced by execution plan(s)")
)

// PrunePromptTemplateService 提供术语精简提示词模板的 CRUD 操作。
type PrunePromptTemplateService struct {
	client *ent.Client
}

// NewPrunePromptTemplateService 创建 PrunePromptTemplateService 实例。
func NewPrunePromptTemplateService(client *ent.Client) *PrunePromptTemplateService {
	return &PrunePromptTemplateService{client: client}
}

// CreatePrunePromptTemplateInput 创建术语精简提示词模板的输入参数。
type CreatePrunePromptTemplateInput struct {
	Name        string
	Description string
	Scope       string // user / org
	OwnerUserID *int
	OwnerOrgID  *int
	Content     string
}

// UpdatePrunePromptTemplateInput 更新术语精简提示词模板的输入参数。
type UpdatePrunePromptTemplateInput struct {
	Name        *string
	Description *string
	Content     *string
}

// ListByUser 列出指定用户的所有术语精简提示词模板（包含内置模板）。
func (s *PrunePromptTemplateService) ListByUser(ctx context.Context, userID int) ([]*ent.PrunePromptTemplate, error) {
	dbTemplates, err := s.client.PrunePromptTemplate.Query().
		Where(
			pruneprompttemplate.ScopeEQ("user"),
			pruneprompttemplate.OwnerUserIDEQ(userID),
		).
		Order(ent.Asc(pruneprompttemplate.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list prune prompt templates: %w", err)
	}
	return append(templates.BuiltinPrunePromptTemplates(), dbTemplates...), nil
}

// ListByOrg 列出指定组织的所有术语精简提示词模板（包含内置模板）。
func (s *PrunePromptTemplateService) ListByOrg(ctx context.Context, orgID int) ([]*ent.PrunePromptTemplate, error) {
	dbTemplates, err := s.client.PrunePromptTemplate.Query().
		Where(
			pruneprompttemplate.ScopeEQ("org"),
			pruneprompttemplate.OwnerOrgIDEQ(orgID),
		).
		Order(ent.Asc(pruneprompttemplate.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list prune prompt templates: %w", err)
	}
	return append(templates.BuiltinPrunePromptTemplates(), dbTemplates...), nil
}

// GetByID 根据 ID 获取术语精简提示词模板（支持内置模板）。
func (s *PrunePromptTemplateService) GetByID(ctx context.Context, id int) (*ent.PrunePromptTemplate, error) {
	if templates.IsBuiltinID(id) {
		pt := templates.BuiltinPrunePromptTemplate(id)
		if pt == nil {
			return nil, ErrPrunePromptTemplateNotFound
		}
		return pt, nil
	}
	pt, err := s.client.PrunePromptTemplate.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrPrunePromptTemplateNotFound
		}
		return nil, fmt.Errorf("query prune prompt template: %w", err)
	}
	return pt, nil
}

// Create 创建术语精简提示词模板。
func (s *PrunePromptTemplateService) Create(ctx context.Context, input CreatePrunePromptTemplateInput) (*ent.PrunePromptTemplate, error) {
	if input.Scope == "" {
		input.Scope = "user"
	}
	if input.Scope != "user" && input.Scope != "org" && input.Scope != "system" {
		return nil, ErrPrunePromptTemplateScopeInvalid
	}

	create := s.client.PrunePromptTemplate.Create().
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
		return nil, fmt.Errorf("create prune prompt template: %w", err)
	}
	return pt, nil
}

// Update 更新术语精简提示词模板（内置模板不可修改）。
func (s *PrunePromptTemplateService) Update(ctx context.Context, id int, input UpdatePrunePromptTemplateInput) (*ent.PrunePromptTemplate, error) {
	if templates.IsBuiltinID(id) {
		return nil, ErrPrunePromptTemplateNotFound
	}
	pt, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if pt.Scope == "system" {
		return nil, ErrPrunePromptTemplateNotFound // 系统模板不可修改
	}

	update := s.client.PrunePromptTemplate.UpdateOneID(id)

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
		return nil, fmt.Errorf("update prune prompt template: %w", err)
	}
	return updated, nil
}

// Delete 删除术语精简提示词模板（内置模板不可删除）。
func (s *PrunePromptTemplateService) Delete(ctx context.Context, id int) error {
	if templates.IsBuiltinID(id) {
		return ErrPrunePromptTemplateNotFound
	}
	pt, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if pt.Scope == "system" {
		return ErrPrunePromptTemplateNotFound // 系统模板不可删除
	}

	// 术语精简提示词模板当前不被执行计划引用，预留检查逻辑。
	// 若未来执行计划支持精简轮次，可在此检查引用关系。

	return s.client.PrunePromptTemplate.DeleteOneID(id).Exec(ctx)
}
