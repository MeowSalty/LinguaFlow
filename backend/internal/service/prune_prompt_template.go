package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/pruneprompttemplate"

	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrPrunePromptTemplateNotFound     = errors.New("prune_prompt_template not found")
	ErrPrunePromptTemplateScopeInvalid = errors.New("prune_prompt_template scope invalid")
	ErrPrunePromptTemplateInUse        = errors.New("prune_prompt_template is referenced by execution plan(s)")
)

// PrunePromptTemplateService manages personal and organization templates.
type PrunePromptTemplateService struct{ client *ent.Client }

func NewPrunePromptTemplateService(client *ent.Client) *PrunePromptTemplateService {
	return &PrunePromptTemplateService{client: client}
}

type CreatePrunePromptTemplateInput struct {
	Name        string
	Description string
	OrgID       *int
	Content     string
}
type UpdatePrunePromptTemplateInput struct {
	Name        *string
	Description *string
	Content     *string
}

// ListByUser preserves the existing personal plus builtin list.
func (s *PrunePromptTemplateService) ListByUser(ctx context.Context, userID int) ([]*ent.PrunePromptTemplate, error) {
	if userID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := s.client.PrunePromptTemplate.Query().Where(
		pruneprompttemplate.ScopeEQ(ScopeUser), pruneprompttemplate.OwnerUserIDEQ(userID), pruneprompttemplate.OwnerOrgIDIsNil(),
	).Order(ent.Asc(pruneprompttemplate.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list prune_prompt_template: %w", err)
	}
	return append(templates.BuiltinPrunePromptTemplates(), rows...), nil
}

// ListByOrg returns only the requested organization's templates.
func (s *PrunePromptTemplateService) ListByOrg(ctx context.Context, actorID, orgID int) ([]*ent.PrunePromptTemplate, error) {
	if err := requireSharedOrganization(ctx, s.client, actorID, orgID, false); err != nil {
		return nil, err
	}
	return s.client.PrunePromptTemplate.Query().Where(
		pruneprompttemplate.ScopeEQ(ScopeOrg), pruneprompttemplate.OwnerOrgIDEQ(orgID), pruneprompttemplate.OwnerUserIDIsNil(),
	).Order(ent.Asc(pruneprompttemplate.FieldID)).All(ctx)
}

func (s *PrunePromptTemplateService) GetByID(ctx context.Context, actorID, id int) (*ent.PrunePromptTemplate, error) {
	var row *ent.PrunePromptTemplate
	if templates.IsBuiltinID(id) {
		row = templates.BuiltinPrunePromptTemplate(id)
		if row == nil {
			return nil, ErrPrunePromptTemplateNotFound
		}
	} else {
		var err error
		row, err = s.client.PrunePromptTemplate.Get(ctx, id)
		if err != nil {
			return nil, sharedAccessError(err, ErrPrunePromptTemplateNotFound)
		}
	}
	if err := checkSharedAccess(ctx, s.client, actorID, row.Scope, row.OwnerUserID, row.OwnerOrgID, false); err != nil {
		return nil, sharedAccessError(err, ErrPrunePromptTemplateNotFound)
	}
	return row, nil
}

func (s *PrunePromptTemplateService) Create(ctx context.Context, actorID int, input CreatePrunePromptTemplateInput) (*ent.PrunePromptTemplate, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || actorID <= 0 {
		return nil, ErrInvalidInput
	}
	var row *ent.PrunePromptTemplate
	err := withSharedMutation(ctx, s.client, input.OrgID, func(client *ent.Client) error {
		scope, err := sharedCreateScope(ctx, client, actorID, input.OrgID)
		if err != nil {
			return err
		}
		create := client.PrunePromptTemplate.Create().SetName(name).SetDescription(input.Description).SetScope(scope).SetContent(input.Content)
		if input.OrgID == nil {
			create.SetOwnerUserID(actorID)
		} else {
			create.SetOwnerOrgID(*input.OrgID)
		}
		row, err = create.Save(ctx)
		if err != nil {
			return err
		}
		return recordSharedAudit(ctx, client, actorID, input.OrgID, "prune_prompt_template", "create", row.ID)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *PrunePromptTemplateService) Update(ctx context.Context, actorID, id int, input UpdatePrunePromptTemplateInput) (*ent.PrunePromptTemplate, error) {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return nil, err
	}
	var row *ent.PrunePromptTemplate
	err = withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &PrunePromptTemplateService{client: client}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrPrunePromptTemplateNotFound)
		}
		update := client.PrunePromptTemplate.UpdateOneID(id)
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
			return sharedAccessError(err, ErrPrunePromptTemplateNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "prune_prompt_template", "update", row.ID)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *PrunePromptTemplateService) Delete(ctx context.Context, actorID, id int) error {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return err
	}
	return withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &PrunePromptTemplateService{client: client}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrPrunePromptTemplateNotFound)
		}

		if err := client.PrunePromptTemplate.DeleteOneID(id).Exec(ctx); err != nil {
			return sharedAccessError(err, ErrPrunePromptTemplateNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "prune_prompt_template", "delete", id)
	})
}
