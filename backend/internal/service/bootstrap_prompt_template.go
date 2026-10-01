package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bootstrapprompttemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/executionplantemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrBootstrapPromptTemplateNotFound     = errors.New("bootstrap_prompt_template not found")
	ErrBootstrapPromptTemplateScopeInvalid = errors.New("bootstrap_prompt_template scope invalid")
	ErrBootstrapPromptTemplateInUse        = errors.New("bootstrap_prompt_template is referenced by execution plan(s)")
)

// BootstrapPromptTemplateService manages personal and organization templates.
type BootstrapPromptTemplateService struct{ client *ent.Client }

func NewBootstrapPromptTemplateService(client *ent.Client) *BootstrapPromptTemplateService {
	return &BootstrapPromptTemplateService{client: client}
}

type CreateBootstrapPromptTemplateInput struct {
	Name        string
	Description string
	OrgID       *int
	Content     string
}
type UpdateBootstrapPromptTemplateInput struct {
	Name        *string
	Description *string
	Content     *string
}

// ListByUser preserves the existing personal plus builtin list.
func (s *BootstrapPromptTemplateService) ListByUser(ctx context.Context, userID int) ([]*ent.BootstrapPromptTemplate, error) {
	if userID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := s.client.BootstrapPromptTemplate.Query().Where(
		bootstrapprompttemplate.ScopeEQ(ScopeUser), bootstrapprompttemplate.OwnerUserIDEQ(userID), bootstrapprompttemplate.OwnerOrgIDIsNil(),
	).Order(ent.Asc(bootstrapprompttemplate.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list bootstrap_prompt_template: %w", err)
	}
	return append(templates.BuiltinBootstrapPromptTemplates(), rows...), nil
}

// ListByOrg returns only the requested organization's templates.
func (s *BootstrapPromptTemplateService) ListByOrg(ctx context.Context, actorID, orgID int) ([]*ent.BootstrapPromptTemplate, error) {
	if err := requireSharedOrganization(ctx, s.client, actorID, orgID, false); err != nil {
		return nil, err
	}
	return s.client.BootstrapPromptTemplate.Query().Where(
		bootstrapprompttemplate.ScopeEQ(ScopeOrg), bootstrapprompttemplate.OwnerOrgIDEQ(orgID), bootstrapprompttemplate.OwnerUserIDIsNil(),
	).Order(ent.Asc(bootstrapprompttemplate.FieldID)).All(ctx)
}

func (s *BootstrapPromptTemplateService) GetByID(ctx context.Context, actorID, id int) (*ent.BootstrapPromptTemplate, error) {
	var row *ent.BootstrapPromptTemplate
	if templates.IsBuiltinID(id) {
		row = templates.BuiltinBootstrapPromptTemplate(id)
		if row == nil {
			return nil, ErrBootstrapPromptTemplateNotFound
		}
	} else {
		var err error
		row, err = s.client.BootstrapPromptTemplate.Get(ctx, id)
		if err != nil {
			return nil, sharedAccessError(err, ErrBootstrapPromptTemplateNotFound)
		}
	}
	if err := checkSharedAccess(ctx, s.client, actorID, row.Scope, row.OwnerUserID, row.OwnerOrgID, false); err != nil {
		return nil, sharedAccessError(err, ErrBootstrapPromptTemplateNotFound)
	}
	return row, nil
}

func (s *BootstrapPromptTemplateService) Create(ctx context.Context, actorID int, input CreateBootstrapPromptTemplateInput) (*ent.BootstrapPromptTemplate, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || actorID <= 0 {
		return nil, ErrInvalidInput
	}
	var row *ent.BootstrapPromptTemplate
	err := withSharedMutation(ctx, s.client, input.OrgID, func(client *ent.Client) error {
		scope, err := sharedCreateScope(ctx, client, actorID, input.OrgID)
		if err != nil {
			return err
		}
		create := client.BootstrapPromptTemplate.Create().SetName(name).SetDescription(input.Description).SetScope(scope).SetContent(input.Content)
		if input.OrgID == nil {
			create.SetOwnerUserID(actorID)
		} else {
			create.SetOwnerOrgID(*input.OrgID)
		}
		row, err = create.Save(ctx)
		if err != nil {
			return err
		}
		return recordSharedAudit(ctx, client, actorID, input.OrgID, "bootstrap_prompt_template", "create", row.ID)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *BootstrapPromptTemplateService) Update(ctx context.Context, actorID, id int, input UpdateBootstrapPromptTemplateInput) (*ent.BootstrapPromptTemplate, error) {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return nil, err
	}
	var row *ent.BootstrapPromptTemplate
	err = withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &BootstrapPromptTemplateService{client: client}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrBootstrapPromptTemplateNotFound)
		}
		update := client.BootstrapPromptTemplate.UpdateOneID(id)
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
		if input.Content != nil {
			update.SetContent(*input.Content)
		}
		row, err = update.Save(ctx)
		if err != nil {
			return sharedAccessError(err, ErrBootstrapPromptTemplateNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "bootstrap_prompt_template", "update", row.ID)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *BootstrapPromptTemplateService) Delete(ctx context.Context, actorID, id int) error {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return err
	}
	return withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &BootstrapPromptTemplateService{client: client}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrBootstrapPromptTemplateNotFound)
		}
		plans, err := client.ExecutionPlanTemplate.Query().Select(executionplantemplate.FieldRounds).All(ctx)
		if err != nil {
			return err
		}
		for _, plan := range plans {
			for _, round := range plan.Rounds {
				if round.Mode == "extract" && round.Extract != nil && round.Extract.BootstrapTemplateID == id {
					return ErrBootstrapPromptTemplateInUse
				}
			}
		}
		if err := client.BootstrapPromptTemplate.DeleteOneID(id).Exec(ctx); err != nil {
			return sharedAccessError(err, ErrBootstrapPromptTemplateNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "bootstrap_prompt_template", "delete", id)
	})
}
