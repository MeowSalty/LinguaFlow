package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/executionplantemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrExecutionPlanNotFound      = errors.New("execution plan template not found")
	ErrExecutionPlanScopeInvalid  = errors.New("execution plan template scope invalid")
	ErrExecutionPlanConfigInvalid = errors.New("execution plan template config invalid")
	ErrExecutionPlanInUse         = errors.New("execution plan template is referenced by translation jobs")
)

// ExecutionPlanService 执行计划模板服务。
type ExecutionPlanService struct {
	client *ent.Client
	users  *UserService
}

// NewExecutionPlanService 创建执行计划模板服务。
func NewExecutionPlanService(client *ent.Client, users *UserService) *ExecutionPlanService {
	return &ExecutionPlanService{client: client, users: users}
}

// CreateExecutionPlanTemplateInput 创建执行计划模板的输入参数。
type CreateExecutionPlanTemplateInput struct {
	Name        string                              `json:"name"`
	Description string                              `json:"description"`
	Scope       string                              `json:"scope"` // user / org
	OwnerUserID *int                                `json:"owner_user_id,omitempty"`
	OwnerOrgID  *int                                `json:"owner_org_id,omitempty"`
	RubyRetry   schema.ExecutionPlanRubyRetryConfig `json:"ruby_retry"`
	Rounds      []schema.ExecutionRoundConfig       `json:"rounds"`
}

// UpdateExecutionPlanTemplateInput 更新执行计划模板的输入参数。
type UpdateExecutionPlanTemplateInput struct {
	Name        *string                              `json:"name,omitempty"`
	Description *string                              `json:"description,omitempty"`
	RubyRetry   *schema.ExecutionPlanRubyRetryConfig `json:"ruby_retry,omitempty"`
	Rounds      []schema.ExecutionRoundConfig        `json:"rounds,omitempty"`
}

// ListByUser 列出用户可访问的执行计划模板。
// 包括：用户自己的（scope=user）+ 用户所属组织的（scope=org）。
func (s *ExecutionPlanService) ListByUser(ctx context.Context, userID int) ([]*ent.ExecutionPlanTemplate, error) {
	orgIDs, _ := s.client.Organization.Query().
		Where(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(userID)))).
		IDs(ctx)

	var preds []predicate.ExecutionPlanTemplate
	// 用户自己的
	preds = append(preds, executionplantemplate.And(
		executionplantemplate.ScopeEQ(ScopeUser),
		executionplantemplate.OwnerUserIDEQ(userID),
	))
	// 用户所属组织的
	if len(orgIDs) > 0 {
		preds = append(preds, executionplantemplate.And(
			executionplantemplate.ScopeEQ(ScopeOrg),
			executionplantemplate.OwnerOrgIDIn(orgIDs...),
		))
	}

	return s.client.ExecutionPlanTemplate.Query().
		Where(executionplantemplate.Or(preds...)).
		Order(ent.Asc(executionplantemplate.FieldID)).
		All(ctx)
}

// ListByOrg 列出指定组织的所有执行计划模板。
func (s *ExecutionPlanService) ListByOrg(ctx context.Context, orgID int) ([]*ent.ExecutionPlanTemplate, error) {
	return s.client.ExecutionPlanTemplate.Query().
		Where(
			executionplantemplate.ScopeEQ(ScopeOrg),
			executionplantemplate.OwnerOrgIDEQ(orgID),
		).
		Order(ent.Asc(executionplantemplate.FieldID)).
		All(ctx)
}

// GetByID 获取执行计划模板（带权限校验）。
func (s *ExecutionPlanService) GetByID(ctx context.Context, userID, planID int) (*ent.ExecutionPlanTemplate, error) {
	plan, err := s.client.ExecutionPlanTemplate.Get(ctx, planID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrExecutionPlanNotFound
		}
		return nil, fmt.Errorf("query execution plan template: %w", err)
	}
	if err := s.checkAccess(ctx, userID, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// GetByIDRaw 根据 ID 获取执行计划模板（不做权限校验，供内部调用）。
func (s *ExecutionPlanService) GetByIDRaw(ctx context.Context, planID int) (*ent.ExecutionPlanTemplate, error) {
	plan, err := s.client.ExecutionPlanTemplate.Get(ctx, planID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrExecutionPlanNotFound
		}
		return nil, fmt.Errorf("query execution plan template: %w", err)
	}
	return plan, nil
}

// Create 创建执行计划模板。
func (s *ExecutionPlanService) Create(ctx context.Context, input CreateExecutionPlanTemplateInput) (*ent.ExecutionPlanTemplate, error) {
	if input.Scope == "" {
		input.Scope = ScopeUser
	}
	if input.Scope != ScopeUser && input.Scope != ScopeOrg {
		return nil, ErrExecutionPlanScopeInvalid
	}

	if err := validateExecutionRounds(input.Rounds); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, ErrInvalidInput
	}

	create := s.client.ExecutionPlanTemplate.Create().
		SetName(name).
		SetDescription(strings.TrimSpace(input.Description)).
		SetScope(input.Scope).
		SetRubyRetry(input.RubyRetry).
		SetRounds(input.Rounds)

	switch input.Scope {
	case ScopeUser:
		if input.OwnerUserID == nil {
			return nil, ErrInvalidInput
		}
		create.SetOwnerUserID(*input.OwnerUserID)
	case ScopeOrg:
		if input.OwnerOrgID == nil {
			return nil, ErrInvalidInput
		}
		create.SetOwnerOrgID(*input.OwnerOrgID)
	}

	plan, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create execution plan template: %w", err)
	}
	return plan, nil
}

// Update 更新执行计划模板。
func (s *ExecutionPlanService) Update(ctx context.Context, userID, planID int, input UpdateExecutionPlanTemplateInput) (*ent.ExecutionPlanTemplate, error) {
	plan, err := s.GetByID(ctx, userID, planID)
	if err != nil {
		return nil, err
	}

	if plan.Scope == "system" {
		return nil, ErrExecutionPlanNotFound // 系统模板不可修改
	}

	if input.Rounds != nil {
		if err := validateExecutionRounds(input.Rounds); err != nil {
			return nil, err
		}
	}

	update := s.client.ExecutionPlanTemplate.UpdateOneID(planID)

	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, ErrInvalidInput
		}
		update.SetName(name)
	}
	if input.Description != nil {
		update.SetDescription(strings.TrimSpace(*input.Description))
	}
	if input.RubyRetry != nil {
		update.SetRubyRetry(*input.RubyRetry)
	}
	if input.Rounds != nil {
		update.SetRounds(input.Rounds)
	}

	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrExecutionPlanNotFound
		}
		return nil, fmt.Errorf("update execution plan template: %w", err)
	}
	return updated, nil
}

// Delete 删除执行计划模板。
// 如果有 TranslationJob 引用了该模板，拒绝删除。
func (s *ExecutionPlanService) Delete(ctx context.Context, userID, planID int) error {
	plan, err := s.GetByID(ctx, userID, planID)
	if err != nil {
		return err
	}

	if plan.Scope == "system" {
		return ErrExecutionPlanNotFound // 系统模板不可删除
	}

	// 检查是否有任务引用
	count, err := s.client.Job.Query().
		Where(job.ExecutionPlanIDEQ(plan.ID)).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("check job references: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: %d translation jobs reference it", ErrExecutionPlanInUse, count)
	}

	return s.client.ExecutionPlanTemplate.DeleteOneID(plan.ID).Exec(ctx)
}

// checkAccess 验证用户是否有权访问指定执行计划模板。
func (s *ExecutionPlanService) checkAccess(ctx context.Context, userID int, plan *ent.ExecutionPlanTemplate) error {
	switch plan.Scope {
	case ScopeUser:
		if plan.OwnerUserID == nil || *plan.OwnerUserID != userID {
			return ErrExecutionPlanNotFound // 不泄露资源存在性
		}
	case ScopeOrg:
		if plan.OwnerOrgID == nil {
			return ErrExecutionPlanNotFound
		}
		if _, err := s.users.requireMembership(ctx, userID, *plan.OwnerOrgID, OrgRoleMember); err != nil {
			return err
		}
	default:
		return ErrExecutionPlanScopeInvalid
	}
	return nil
}

// validateExecutionRounds 校验执行轮次配置的有效性。
func validateExecutionRounds(rounds []schema.ExecutionRoundConfig) error {
	if len(rounds) == 0 {
		return fmt.Errorf("%w: rounds must not be empty", ErrExecutionPlanConfigInvalid)
	}
	for i, round := range rounds {
		if strings.TrimSpace(round.Name) == "" {
			return fmt.Errorf("%w: rounds[%d].name must not be empty", ErrExecutionPlanConfigInvalid, i)
		}
		if round.BackendID <= 0 {
			return fmt.Errorf("%w: rounds[%d].backend_id must be positive", ErrExecutionPlanConfigInvalid, i)
		}
		switch round.Mode {
		case "translate":
			if round.Translate == nil {
				return fmt.Errorf("%w: rounds[%d].translate config required when mode=translate", ErrExecutionPlanConfigInvalid, i)
			}
			t := round.Translate
			if t.PromptTemplateID == 0 {
				return fmt.Errorf("%w: rounds[%d].translate.prompt_template_id must not be zero", ErrExecutionPlanConfigInvalid, i)
			}
			if t.PromptTemplateID < 0 && t.PromptTemplateID != templates.BuiltinTranslationPromptTemplateID {
				return fmt.Errorf("%w: rounds[%d].translate.prompt_template_id %d is not a valid builtin translation template", ErrExecutionPlanConfigInvalid, i, t.PromptTemplateID)
			}
			if t.ProfileID == 0 {
				return fmt.Errorf("%w: rounds[%d].translate.profile_id must not be zero", ErrExecutionPlanConfigInvalid, i)
			}
			if t.ProfileID < 0 && !templates.IsBuiltinID(t.ProfileID) {
				return fmt.Errorf("%w: rounds[%d].translate.profile_id %d is not a valid builtin template", ErrExecutionPlanConfigInvalid, i, t.ProfileID)
			}
			if t.BatchSize < 0 {
				return fmt.Errorf("%w: rounds[%d].translate.batch_size must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if t.MaxWordsPerBatch < 0 {
				return fmt.Errorf("%w: rounds[%d].translate.max_words_per_batch must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if t.BatchSize <= 0 && t.MaxWordsPerBatch <= 0 {
				return fmt.Errorf("%w: rounds[%d].translate.batch_size and max_words_per_batch cannot both be 0", ErrExecutionPlanConfigInvalid, i)
			}
			if t.Concurrency < 1 {
				return fmt.Errorf("%w: rounds[%d].translate.concurrency must be >= 1", ErrExecutionPlanConfigInvalid, i)
			}
			if t.FallbackShrink < 0 || t.FallbackShrink >= 1 {
				return fmt.Errorf("%w: rounds[%d].translate.fallback_shrink must be in [0, 1)", ErrExecutionPlanConfigInvalid, i)
			}
		case "extract":
			if round.Extract == nil {
				return fmt.Errorf("%w: rounds[%d].extract config required when mode=extract", ErrExecutionPlanConfigInvalid, i)
			}
			e := round.Extract
			if e.BootstrapTemplateID == 0 {
				return fmt.Errorf("%w: rounds[%d].extract.bootstrap_template_id must not be zero", ErrExecutionPlanConfigInvalid, i)
			}
			if e.BootstrapTemplateID < 0 && e.BootstrapTemplateID != templates.BuiltinBootstrapPromptTemplateID {
				return fmt.Errorf("%w: rounds[%d].extract.bootstrap_template_id %d is not a valid builtin bootstrap template", ErrExecutionPlanConfigInvalid, i, e.BootstrapTemplateID)
			}
			if e.BatchSize < 0 {
				return fmt.Errorf("%w: rounds[%d].extract.batch_size must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if e.Concurrency < 1 {
				return fmt.Errorf("%w: rounds[%d].extract.concurrency must be >= 1", ErrExecutionPlanConfigInvalid, i)
			}
		default:
			return fmt.Errorf("%w: rounds[%d].mode must be 'translate' or 'extract'", ErrExecutionPlanConfigInvalid, i)
		}
	}
	return nil
}
