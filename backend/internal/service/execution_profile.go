package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/executionplantemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/executionprofile"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrExecutionProfileNotFound      = errors.New("execution profile not found")
	ErrExecutionProfileScopeInvalid  = errors.New("execution profile scope invalid")
	ErrExecutionProfileConfigInvalid = errors.New("execution profile config invalid")
	ErrExecutionProfileInUse         = errors.New("execution profile is referenced by execution plan(s)")
)

type ExecutionProfileService struct {
	client *ent.Client
	users  *UserService
}

func NewExecutionProfileService(client *ent.Client, users *UserService) *ExecutionProfileService {
	return &ExecutionProfileService{client: client, users: users}
}

type CreateExecutionProfileInput struct {
	Name        string
	Description string
	OrgID       *int
	Config      *schema.ExecutionProfileConfigData
}
type UpdateExecutionProfileInput struct {
	Name        *string
	Description *string
	Config      *schema.ExecutionProfileConfigData
}

// ListByUser preserves the existing personal plus builtin list.
func (s *ExecutionProfileService) ListByUser(ctx context.Context, userID int) ([]*ent.ExecutionProfile, error) {
	if userID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := s.client.ExecutionProfile.Query().Where(
		executionprofile.ScopeEQ(ScopeUser), executionprofile.OwnerUserIDEQ(userID), executionprofile.OwnerOrgIDIsNil(),
	).Order(ent.Asc(executionprofile.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	rows = append(templates.BuiltinExecutionProfiles(), rows...)
	for _, p := range rows {
		p.Config.NormalizePreserveKinds()
	}
	return rows, nil
}

func (s *ExecutionProfileService) ListByOrg(ctx context.Context, actorID, orgID int) ([]*ent.ExecutionProfile, error) {
	if err := requireSharedOrganization(ctx, s.client, actorID, orgID, false); err != nil {
		return nil, err
	}
	rows, err := s.client.ExecutionProfile.Query().Where(
		executionprofile.ScopeEQ(ScopeOrg), executionprofile.OwnerOrgIDEQ(orgID), executionprofile.OwnerUserIDIsNil(),
	).Order(ent.Asc(executionprofile.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		p.Config.NormalizePreserveKinds()
	}
	return rows, nil
}

func (s *ExecutionProfileService) GetByID(ctx context.Context, actorID, id int) (*ent.ExecutionProfile, error) {
	var row *ent.ExecutionProfile
	if templates.IsBuiltinID(id) {
		row = templates.BuiltinExecutionProfile(id)
		if row == nil {
			return nil, ErrExecutionProfileNotFound
		}
	} else {
		var err error
		row, err = s.client.ExecutionProfile.Get(ctx, id)
		if err != nil {
			return nil, sharedAccessError(err, ErrExecutionProfileNotFound)
		}
	}
	if err := s.CheckAccess(ctx, actorID, row); err != nil {
		return nil, err
	}
	row.Config.NormalizePreserveKinds()
	return row, nil
}

func (s *ExecutionProfileService) Create(ctx context.Context, actorID int, input CreateExecutionProfileInput) (*ent.ExecutionProfile, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || actorID <= 0 {
		return nil, ErrInvalidInput
	}
	if input.Config != nil {
		if err := validateProfileConfig(input.Config); err != nil {
			return nil, err
		}
	}
	var row *ent.ExecutionProfile
	err := withSharedMutation(ctx, s.client, input.OrgID, func(client *ent.Client) error {
		scope, err := sharedCreateScope(ctx, client, actorID, input.OrgID)
		if err != nil {
			return err
		}
		create := client.ExecutionProfile.Create().SetName(name).SetDescription(input.Description).SetScope(scope)
		if input.OrgID == nil {
			create.SetOwnerUserID(actorID)
		} else {
			create.SetOwnerOrgID(*input.OrgID)
		}
		if input.Config != nil {
			create.SetConfig(*input.Config)
		}
		row, err = create.Save(ctx)
		if err != nil {
			return err
		}
		return recordSharedAudit(ctx, client, actorID, input.OrgID, "execution_profile", "create", row.ID)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *ExecutionProfileService) Update(ctx context.Context, actorID, id int, input UpdateExecutionProfileInput) (*ent.ExecutionProfile, error) {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return nil, err
	}
	var row *ent.ExecutionProfile
	err = withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &ExecutionProfileService{client: client, users: s.users}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrExecutionProfileNotFound)
		}
		if input.Config != nil {
			if err := validateProfileConfig(input.Config); err != nil {
				return err
			}
		}
		update := client.ExecutionProfile.UpdateOneID(id)
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
		if input.Config != nil {
			update.SetConfig(*input.Config)
		}
		row, err = update.Save(ctx)
		if err != nil {
			return sharedAccessError(err, ErrExecutionProfileNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "execution_profile", "update", id)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *ExecutionProfileService) Delete(ctx context.Context, actorID, id int) error {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return err
	}
	return withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &ExecutionProfileService{client: client, users: s.users}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrExecutionProfileNotFound)
		}
		referenced, err := client.ExecutionPlanTemplate.Query().Where(executionplantemplate.ProfileID(id)).Exist(ctx)
		if err != nil {
			return err
		}
		if referenced {
			return ErrExecutionProfileInUse
		}
		if err := client.ExecutionProfile.DeleteOneID(id).Exec(ctx); err != nil {
			return sharedAccessError(err, ErrExecutionProfileNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "execution_profile", "delete", id)
	})
}

// CheckAccess is the read authorization used by plan references and runtime snapshots.
func (s *ExecutionProfileService) CheckAccess(ctx context.Context, actorID int, profile *ent.ExecutionProfile) error {
	return sharedAccessError(checkSharedAccess(ctx, s.client, actorID, profile.Scope, profile.OwnerUserID, profile.OwnerOrgID, false), ErrExecutionProfileNotFound)
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
