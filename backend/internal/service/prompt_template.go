package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/prompttemplate"
)

var (
	ErrPromptTemplateNotFound     = errors.New("prompt template not found")
	ErrPromptTemplateScopeInvalid = errors.New("prompt template scope invalid")
)

// PromptTemplateService 提供提示词模板的 CRUD 操作。
type PromptTemplateService struct {
	client *ent.Client
}

// NewPromptTemplateService 创建 PromptTemplateService 实例。
func NewPromptTemplateService(client *ent.Client) *PromptTemplateService {
	return &PromptTemplateService{client: client}
}

// CreatePromptTemplateInput 创建提示词模板的输入参数。
type CreatePromptTemplateInput struct {
	Name                string
	Description         string
	Scope               string // user / org
	OwnerUserID         *int
	OwnerOrgID          *int
	SystemPromptContent string
}

// UpdatePromptTemplateInput 更新提示词模板的输入参数。
type UpdatePromptTemplateInput struct {
	Name                *string
	Description         *string
	SystemPromptContent *string
}

// ListByUser 列出指定用户的所有提示词模板。
func (s *PromptTemplateService) ListByUser(ctx context.Context, userID int) ([]*ent.PromptTemplate, error) {
	return s.client.PromptTemplate.Query().
		Where(
			prompttemplate.ScopeEQ("user"),
			prompttemplate.OwnerUserIDEQ(userID),
		).
		Order(ent.Asc(prompttemplate.FieldID)).
		All(ctx)
}

// ListByOrg 列出指定组织的所有提示词模板。
func (s *PromptTemplateService) ListByOrg(ctx context.Context, orgID int) ([]*ent.PromptTemplate, error) {
	return s.client.PromptTemplate.Query().
		Where(
			prompttemplate.ScopeEQ("org"),
			prompttemplate.OwnerOrgIDEQ(orgID),
		).
		Order(ent.Asc(prompttemplate.FieldID)).
		All(ctx)
}

// ListSystem 列出所有系统级提示词模板。
func (s *PromptTemplateService) ListSystem(ctx context.Context) ([]*ent.PromptTemplate, error) {
	return s.client.PromptTemplate.Query().
		Where(prompttemplate.ScopeEQ("system")).
		Order(ent.Asc(prompttemplate.FieldID)).
		All(ctx)
}

// GetByID 根据 ID 获取提示词模板。
func (s *PromptTemplateService) GetByID(ctx context.Context, id int) (*ent.PromptTemplate, error) {
	pt, err := s.client.PromptTemplate.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrPromptTemplateNotFound
		}
		return nil, fmt.Errorf("query prompt template: %w", err)
	}
	return pt, nil
}

// Create 创建提示词模板。
func (s *PromptTemplateService) Create(ctx context.Context, input CreatePromptTemplateInput) (*ent.PromptTemplate, error) {
	if input.Scope == "" {
		input.Scope = "user"
	}
	if input.Scope != "user" && input.Scope != "org" && input.Scope != "system" {
		return nil, ErrPromptTemplateScopeInvalid
	}

	create := s.client.PromptTemplate.Create().
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
		return nil, fmt.Errorf("create prompt template: %w", err)
	}
	return pt, nil
}

// Update 更新提示词模板。
func (s *PromptTemplateService) Update(ctx context.Context, id int, input UpdatePromptTemplateInput) (*ent.PromptTemplate, error) {
	pt, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if pt.Scope == "system" {
		return nil, ErrPromptTemplateNotFound // 系统模板不可修改
	}

	update := s.client.PromptTemplate.UpdateOneID(id)

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
		return nil, fmt.Errorf("update prompt template: %w", err)
	}
	return updated, nil
}

// Delete 删除提示词模板。
func (s *PromptTemplateService) Delete(ctx context.Context, id int) error {
	pt, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if pt.Scope == "system" {
		return ErrPromptTemplateNotFound // 系统模板不可删除
	}
	return s.client.PromptTemplate.DeleteOneID(id).Exec(ctx)
}
