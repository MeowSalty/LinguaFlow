package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/executionplantemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationprompttemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrTranslationPromptTemplateNotFound     = errors.New("translation_prompt_template not found")
	ErrTranslationPromptTemplateScopeInvalid = errors.New("translation_prompt_template scope invalid")
	ErrTranslationPromptTemplateInUse        = errors.New("translation_prompt_template is referenced by execution plan(s)")
)

// TranslationPromptTemplateService manages personal and organization templates.
type TranslationPromptTemplateService struct{ client *ent.Client }

func NewTranslationPromptTemplateService(client *ent.Client) *TranslationPromptTemplateService {
	return &TranslationPromptTemplateService{client: client}
}

type CreateTranslationPromptTemplateInput struct {
	Name                string
	Description         string
	OrgID               *int
	SystemPromptContent string
}
type UpdateTranslationPromptTemplateInput struct {
	Name                *string
	Description         *string
	SystemPromptContent *string
}

// ListByUser preserves the existing personal plus builtin list.
func (s *TranslationPromptTemplateService) ListByUser(ctx context.Context, userID int) ([]*ent.TranslationPromptTemplate, error) {
	if userID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := s.client.TranslationPromptTemplate.Query().Where(
		translationprompttemplate.ScopeEQ(ScopeUser), translationprompttemplate.OwnerUserIDEQ(userID), translationprompttemplate.OwnerOrgIDIsNil(),
	).Order(ent.Asc(translationprompttemplate.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list translation_prompt_template: %w", err)
	}
	return append(templates.BuiltinTranslationPromptTemplates(), rows...), nil
}

// ListByOrg returns only the requested organization's templates.
func (s *TranslationPromptTemplateService) ListByOrg(ctx context.Context, actorID, orgID int) ([]*ent.TranslationPromptTemplate, error) {
	if err := requireSharedOrganization(ctx, s.client, actorID, orgID, false); err != nil {
		return nil, err
	}
	return s.client.TranslationPromptTemplate.Query().Where(
		translationprompttemplate.ScopeEQ(ScopeOrg), translationprompttemplate.OwnerOrgIDEQ(orgID), translationprompttemplate.OwnerUserIDIsNil(),
	).Order(ent.Asc(translationprompttemplate.FieldID)).All(ctx)
}

func (s *TranslationPromptTemplateService) GetByID(ctx context.Context, actorID, id int) (*ent.TranslationPromptTemplate, error) {
	var row *ent.TranslationPromptTemplate
	if templates.IsBuiltinID(id) {
		row = templates.BuiltinTranslationPromptTemplate(id)
		if row == nil {
			return nil, ErrTranslationPromptTemplateNotFound
		}
	} else {
		var err error
		row, err = s.client.TranslationPromptTemplate.Get(ctx, id)
		if err != nil {
			return nil, sharedAccessError(err, ErrTranslationPromptTemplateNotFound)
		}
	}
	if err := checkSharedAccess(ctx, s.client, actorID, row.Scope, row.OwnerUserID, row.OwnerOrgID, false); err != nil {
		return nil, sharedAccessError(err, ErrTranslationPromptTemplateNotFound)
	}
	return row, nil
}

func (s *TranslationPromptTemplateService) Create(ctx context.Context, actorID int, input CreateTranslationPromptTemplateInput) (*ent.TranslationPromptTemplate, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || actorID <= 0 {
		return nil, ErrInvalidInput
	}
	var row *ent.TranslationPromptTemplate
	err := withSharedMutation(ctx, s.client, input.OrgID, func(client *ent.Client) error {
		scope, err := sharedCreateScope(ctx, client, actorID, input.OrgID)
		if err != nil {
			return err
		}
		create := client.TranslationPromptTemplate.Create().SetName(name).SetDescription(input.Description).SetScope(scope).SetSystemPromptContent(input.SystemPromptContent)
		if input.OrgID == nil {
			create.SetOwnerUserID(actorID)
		} else {
			create.SetOwnerOrgID(*input.OrgID)
		}
		row, err = create.Save(ctx)
		if err != nil {
			return err
		}
		return recordSharedAudit(ctx, client, actorID, input.OrgID, "translation_prompt_template", "create", row.ID)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *TranslationPromptTemplateService) Update(ctx context.Context, actorID, id int, input UpdateTranslationPromptTemplateInput) (*ent.TranslationPromptTemplate, error) {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return nil, err
	}
	var row *ent.TranslationPromptTemplate
	err = withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &TranslationPromptTemplateService{client: client}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrTranslationPromptTemplateNotFound)
		}
		update := client.TranslationPromptTemplate.UpdateOneID(id)
		if input.Name != nil {
			name := strings.TrimSpace(*input.Name)
			if name == "" {
				return ErrInvalidInput
			}
			update.SetName(name)
		}
		if input.Description != nil {
			update.SetDescription(*input.Description)
		}
		if input.SystemPromptContent != nil {
			update.SetSystemPromptContent(*input.SystemPromptContent)
		}
		row, err = update.Save(ctx)
		if err != nil {
			return sharedAccessError(err, ErrTranslationPromptTemplateNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "translation_prompt_template", "update", row.ID)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *TranslationPromptTemplateService) Delete(ctx context.Context, actorID, id int) error {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return err
	}
	return withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &TranslationPromptTemplateService{client: client}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrTranslationPromptTemplateNotFound)
		}
		plans, err := client.ExecutionPlanTemplate.Query().Select(executionplantemplate.FieldRounds).All(ctx)
		if err != nil {
			return err
		}
		for _, plan := range plans {
			for _, round := range plan.Rounds {
				if round.Mode == "translate" && round.Translate != nil && round.Translate.PromptTemplateID == id {
					return ErrTranslationPromptTemplateInUse
				}
			}
		}
		if err := client.TranslationPromptTemplate.DeleteOneID(id).Exec(ctx); err != nil {
			return sharedAccessError(err, ErrTranslationPromptTemplateNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "translation_prompt_template", "delete", id)
	})
}
