package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/executionplantemplate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

var (
	ErrExecutionPlanNotFound      = errors.New("execution plan template not found")
	ErrExecutionPlanScopeInvalid  = errors.New("execution plan template scope invalid")
	ErrExecutionPlanConfigInvalid = errors.New("execution plan template config invalid")
	ErrExecutionPlanInUse         = errors.New("execution plan template is referenced by translation jobs")
)

type ExecutionPlanService struct {
	client   *ent.Client
	users    *UserService
	profiles *ExecutionProfileService
}

func NewExecutionPlanService(client *ent.Client, users *UserService, profiles *ExecutionProfileService) *ExecutionPlanService {
	return &ExecutionPlanService{client: client, users: users, profiles: profiles}
}

type CreateExecutionPlanTemplateInput struct {
	Name        string                              `json:"name"`
	Description string                              `json:"description"`
	OrgID       *int                                `json:"org_id,omitempty"`
	ProfileID   int                                 `json:"profile_id"`
	RubyRetry   schema.ExecutionPlanRubyRetryConfig `json:"ruby_retry"`
	Rounds      []schema.ExecutionRoundConfig       `json:"rounds"`
}
type UpdateExecutionPlanTemplateInput struct {
	Name        *string                              `json:"name,omitempty"`
	Description *string                              `json:"description,omitempty"`
	ProfileID   *int                                 `json:"profile_id,omitempty"`
	RubyRetry   *schema.ExecutionPlanRubyRetryConfig `json:"ruby_retry,omitempty"`
	Rounds      []schema.ExecutionRoundConfig        `json:"rounds,omitempty"`
}

// ListByUser preserves the list of personal and all current organizations' plans.
func (s *ExecutionPlanService) ListByUser(ctx context.Context, userID int) ([]*ent.ExecutionPlanTemplate, error) {
	if userID <= 0 {
		return nil, ErrInvalidInput
	}
	return s.client.ExecutionPlanTemplate.Query().Where(executionplantemplate.Or(
		executionplantemplate.And(executionplantemplate.ScopeEQ(ScopeUser), executionplantemplate.OwnerUserIDEQ(userID), executionplantemplate.OwnerOrgIDIsNil()),
		executionplantemplate.And(executionplantemplate.ScopeEQ(ScopeOrg), executionplantemplate.OwnerUserIDIsNil(),
			executionplantemplate.HasOwnerOrgWith(organization.HasMembershipsWith(
				orgmembership.HasUserWith(user.IDEQ(userID)), orgmembership.RoleIn(OrgRoleMember, OrgRoleAdmin, OrgRoleOwner),
			)),
		),
	)).Order(ent.Asc(executionplantemplate.FieldID)).All(ctx)
}

func (s *ExecutionPlanService) ListByOrg(ctx context.Context, actorID, orgID int) ([]*ent.ExecutionPlanTemplate, error) {
	if err := requireSharedOrganization(ctx, s.client, actorID, orgID, false); err != nil {
		return nil, err
	}
	return s.client.ExecutionPlanTemplate.Query().Where(
		executionplantemplate.ScopeEQ(ScopeOrg), executionplantemplate.OwnerOrgIDEQ(orgID), executionplantemplate.OwnerUserIDIsNil(),
	).Order(ent.Asc(executionplantemplate.FieldID)).All(ctx)
}

func (s *ExecutionPlanService) GetByID(ctx context.Context, actorID, id int) (*ent.ExecutionPlanTemplate, error) {
	row, err := s.GetByIDRaw(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.checkAccess(ctx, actorID, row); err != nil {
		return nil, err
	}
	return row, nil
}

// GetByIDRaw is for trusted internal maintenance; user requests must call GetByID.
func (s *ExecutionPlanService) GetByIDRaw(ctx context.Context, id int) (*ent.ExecutionPlanTemplate, error) {
	row, err := s.client.ExecutionPlanTemplate.Get(ctx, id)
	if err != nil {
		return nil, sharedAccessError(err, ErrExecutionPlanNotFound)
	}
	if row.SchemaVersion != execution.SchemaVersion {
		return nil, fmt.Errorf("%w: unsupported or missing plan schema version", ErrExecutionPlanConfigInvalid)
	}
	return row, nil
}

func (s *ExecutionPlanService) Create(ctx context.Context, actorID int, input CreateExecutionPlanTemplateInput) (*ent.ExecutionPlanTemplate, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || actorID <= 0 {
		return nil, ErrInvalidInput
	}
	if err := validateExecutionRounds(input.Rounds); err != nil {
		return nil, err
	}
	var row *ent.ExecutionPlanTemplate
	err := withSharedMutation(ctx, s.client, input.OrgID, func(client *ent.Client) error {
		scope, err := sharedCreateScope(ctx, client, actorID, input.OrgID)
		if err != nil {
			return err
		}
		bound := &ExecutionPlanService{client: client, users: s.users}
		if err := bound.validatePlanReferences(ctx, actorID, input.OrgID, input.ProfileID, input.RubyRetry, input.Rounds); err != nil {
			return err
		}
		create := client.ExecutionPlanTemplate.Create().SetName(name).SetDescription(strings.TrimSpace(input.Description)).
			SetScope(scope).SetProfileID(input.ProfileID).SetRubyRetry(input.RubyRetry).SetRounds(input.Rounds)
		if input.OrgID == nil {
			create.SetOwnerUserID(actorID)
		} else {
			create.SetOwnerOrgID(*input.OrgID)
		}
		row, err = create.Save(ctx)
		if err != nil {
			return err
		}
		return recordSharedAudit(ctx, client, actorID, input.OrgID, "execution_plan", "create", row.ID)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *ExecutionPlanService) Update(ctx context.Context, actorID, id int, input UpdateExecutionPlanTemplateInput) (*ent.ExecutionPlanTemplate, error) {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return nil, err
	}
	var row *ent.ExecutionPlanTemplate
	err = withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &ExecutionPlanService{client: client, users: s.users}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrExecutionPlanNotFound)
		}
		profileID, rubyRetry, rounds := current.ProfileID, current.RubyRetry, current.Rounds
		if input.ProfileID != nil {
			profileID = *input.ProfileID
		}
		if input.RubyRetry != nil {
			rubyRetry = *input.RubyRetry
		}
		if input.Rounds != nil {
			rounds = input.Rounds
		}
		if err := validateExecutionRounds(rounds); err != nil {
			return err
		}
		if err := bound.validatePlanReferences(ctx, actorID, current.OwnerOrgID, profileID, rubyRetry, rounds); err != nil {
			return err
		}
		update := client.ExecutionPlanTemplate.UpdateOneID(id)
		if input.Name != nil {
			name := strings.TrimSpace(*input.Name)
			if name == "" {
				return ErrInvalidInput
			}
			update.SetName(name)
		}
		if input.Description != nil {
			update.SetDescription(strings.TrimSpace(*input.Description))
		}
		if input.ProfileID != nil {
			update.SetProfileID(profileID)
		}
		if input.RubyRetry != nil {
			update.SetRubyRetry(rubyRetry)
		}
		if input.Rounds != nil {
			update.SetRounds(rounds)
		}
		row, err = update.Save(ctx)
		if err != nil {
			return sharedAccessError(err, ErrExecutionPlanNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "execution_plan", "update", id)
	})
	if err != nil {
		return nil, err
	}
	return row.Unwrap(), nil
}

func (s *ExecutionPlanService) Delete(ctx context.Context, actorID, id int) error {
	original, err := s.GetByID(ctx, actorID, id)
	if err != nil {
		return err
	}
	return withSharedMutation(ctx, s.client, original.OwnerOrgID, func(client *ent.Client) error {
		bound := &ExecutionPlanService{client: client, users: s.users}
		current, err := bound.GetByID(ctx, actorID, id)
		if err != nil {
			return err
		}
		if err := checkSharedAccess(ctx, client, actorID, current.Scope, current.OwnerUserID, current.OwnerOrgID, true); err != nil {
			return sharedAccessError(err, ErrExecutionPlanNotFound)
		}
		referenced, err := client.Job.Query().Where(job.ExecutionPlanIDEQ(id)).Exist(ctx)
		if err != nil {
			return err
		}
		if referenced {
			return ErrExecutionPlanInUse
		}
		if err := client.ExecutionPlanTemplate.DeleteOneID(id).Exec(ctx); err != nil {
			return sharedAccessError(err, ErrExecutionPlanNotFound)
		}
		return recordSharedAudit(ctx, client, actorID, current.OwnerOrgID, "execution_plan", "delete", id)
	})
}

func (s *ExecutionPlanService) checkAccess(ctx context.Context, actorID int, plan *ent.ExecutionPlanTemplate) error {
	return sharedAccessError(checkSharedAccess(ctx, s.client, actorID, plan.Scope, plan.OwnerUserID, plan.OwnerOrgID, false), ErrExecutionPlanNotFound)
}

func validatePlanProfileID(profileID int) error {
	if profileID == 0 {
		return fmt.Errorf("%w: profile_id must not be zero", ErrExecutionPlanConfigInvalid)
	}
	if profileID < 0 && templates.BuiltinExecutionProfile(profileID) == nil {
		return fmt.Errorf("%w: profile_id %d is not a valid builtin template", ErrExecutionPlanConfigInvalid, profileID)
	}
	return nil
}

// validatePlanProfileRef preserves the standalone actor reference check.
func (s *ExecutionPlanService) validatePlanProfileRef(ctx context.Context, actorID, profileID int) error {
	return s.validatePlanProfileReference(ctx, actorID, nil, profileID)
}

func planReferenceError(field string, err error) error {
	if errors.Is(err, ErrForbidden) || errors.Is(err, errSharedObjectNotFound) ||
		errors.Is(err, ErrExecutionProfileNotFound) || errors.Is(err, ErrTranslationPromptTemplateNotFound) ||
		errors.Is(err, ErrBootstrapPromptTemplateNotFound) || ent.IsNotFound(err) {
		return fmt.Errorf("%w: %s not found or unavailable in this scope", ErrExecutionPlanConfigInvalid, field)
	}
	return err
}

func (s *ExecutionPlanService) validatePlanProfileReference(ctx context.Context, actorID int, orgID *int, profileID int) error {
	if err := validatePlanProfileID(profileID); err != nil {
		return err
	}
	profile, err := (&ExecutionProfileService{client: s.client}).GetByID(ctx, actorID, profileID)
	if err != nil {
		return planReferenceError("profile_id", err)
	}
	return planReferenceError("profile_id", validateSharedReference(profile.Scope, profile.OwnerOrgID, orgID))
}

// validatePlanReferences checks all dependency ownership both when saving a plan and
// immediately before producing a runtime snapshot. Organization targets cannot embed
// private or other organizations' content, even if the actor can read that content.
func (s *ExecutionPlanService) validatePlanReferences(ctx context.Context, actorID int, orgID *int, profileID int, rubyRetry schema.ExecutionPlanRubyRetryConfig, rounds []schema.ExecutionRoundConfig) error {
	if err := s.validatePlanProfileReference(ctx, actorID, orgID, profileID); err != nil {
		return err
	}
	checkBackend := func(id int) error {
		row, err := s.client.Backend.Get(ctx, id)
		if err != nil {
			return planReferenceError("backend_id", err)
		}
		if err := checkSharedAccess(ctx, s.client, actorID, row.Scope, row.OwnerUserID, row.OwnerOrgID, false); err != nil {
			return planReferenceError("backend_id", err)
		}
		return planReferenceError("backend_id", validateSharedReference(row.Scope, row.OwnerOrgID, orgID))
	}
	for i, round := range rounds {
		if round.Mode != "correct" {
			if err := checkBackend(round.BackendID); err != nil {
				return fmt.Errorf("rounds[%d]: %w", i, err)
			}
		}
		if round.Mode == "translate" && round.Translate != nil {
			row, err := (&TranslationPromptTemplateService{client: s.client}).GetByID(ctx, actorID, round.Translate.PromptTemplateID)
			if err != nil {
				return planReferenceError("prompt_template_id", err)
			}
			if err := validateSharedReference(row.Scope, row.OwnerOrgID, orgID); err != nil {
				return planReferenceError("prompt_template_id", err)
			}
		}
		if round.Mode == "extract" && round.Extract != nil {
			row, err := (&BootstrapPromptTemplateService{client: s.client}).GetByID(ctx, actorID, round.Extract.BootstrapTemplateID)
			if err != nil {
				return planReferenceError("template_id", err)
			}
			if err := validateSharedReference(row.Scope, row.OwnerOrgID, orgID); err != nil {
				return planReferenceError("template_id", err)
			}
		}
	}
	if rubyRetry.Enabled && rubyRetry.BackendID != 0 {
		if err := checkBackend(rubyRetry.BackendID); err != nil {
			return fmt.Errorf("ruby_retry: %w", err)
		}
	}
	return nil
}

// validateExecutionRounds 校验执行轮次配置的有效性。
func validateExecutionRounds(rounds []schema.ExecutionRoundConfig) error {
	if len(rounds) == 0 {
		return fmt.Errorf("%w: rounds must not be empty", ErrExecutionPlanConfigInvalid)
	}
	for i, round := range rounds {
		// correct 是纯本地轮，无 backend（backend_id 可省略，不校验为正）。
		// 其余 mode 仍要求 backend_id > 0。
		if round.Mode != "correct" && round.BackendID <= 0 {
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
			// NOTE: profile_id 校验已上提到计划级（validatePlanProfileID），轮级不再持有策略引用。
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
			// fallback_shrink 合法域 (0,1]：0 非法（与 OpenAPI exclusiveMinimum:0 一致）。
			// 前端强制填写且禁止 0；省略（schema 零值 0）同样报错，要求显式提供。
			if t.FallbackShrink <= 0 || t.FallbackShrink > 1 {
				return fmt.Errorf("%w: rounds[%d].translate.fallback_shrink must be in (0, 1]", ErrExecutionPlanConfigInvalid, i)
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
			if e.MaxWordsPerBatch < 0 {
				return fmt.Errorf("%w: rounds[%d].extract.max_words_per_batch must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if e.Concurrency < 1 {
				return fmt.Errorf("%w: rounds[%d].extract.concurrency must be >= 1", ErrExecutionPlanConfigInvalid, i)
			}
		// NOTE: fallback_shrink 当前仅 translate 轮支持缩批，extract 不暴露此字段。
		// 若未来需要，在此加 e.FallbackShrink ∈ [0,1] 校验（参考 translate 分支）。
		case "adjudicate":
			if round.Adjudicate == nil {
				return fmt.Errorf("%w: rounds[%d].adjudicate config required when mode=adjudicate", ErrExecutionPlanConfigInvalid, i)
			}
			a := round.Adjudicate
			if a.BatchSize < 0 {
				return fmt.Errorf("%w: rounds[%d].adjudicate.batch_size must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if a.MaxWordsPerBatch < 0 {
				return fmt.Errorf("%w: rounds[%d].adjudicate.max_words_per_batch must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if a.BatchSize <= 0 && a.MaxWordsPerBatch <= 0 {
				return fmt.Errorf("%w: rounds[%d].adjudicate.batch_size and max_words_per_batch cannot both be 0", ErrExecutionPlanConfigInvalid, i)
			}
			if a.Concurrency < 1 {
				return fmt.Errorf("%w: rounds[%d].adjudicate.concurrency must be >= 1", ErrExecutionPlanConfigInvalid, i)
			}
			// NOTE: fallback_shrink 当前仅 translate 轮支持缩批，adjudicate 不暴露此字段。
			// 若未来需要，在此加 a.FallbackShrink ∈ [0,1] 校验（参考 translate 分支）。
			for _, code := range a.AdjudicateCodes {
				if !qa.IsAdjudicableCode(code) {
					return fmt.Errorf("%w: rounds[%d].adjudicate.adjudicate_codes contains invalid code %q (allowed: %s)", ErrExecutionPlanConfigInvalid, i, code, strings.Join(qa.AdjudicableCodes(), ", "))
				}
			}
		case "semantic_qa":
			if round.SemanticQA == nil {
				return fmt.Errorf("%w: rounds[%d].semantic_qa config required when mode=semantic_qa", ErrExecutionPlanConfigInvalid, i)
			}
			s := round.SemanticQA
			if s.BatchSize < 0 {
				return fmt.Errorf("%w: rounds[%d].semantic_qa.batch_size must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if s.MaxWordsPerBatch < 0 {
				return fmt.Errorf("%w: rounds[%d].semantic_qa.max_words_per_batch must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if s.BatchSize <= 0 && s.MaxWordsPerBatch <= 0 {
				return fmt.Errorf("%w: rounds[%d].semantic_qa.batch_size and max_words_per_batch cannot both be 0", ErrExecutionPlanConfigInvalid, i)
			}
			if s.Concurrency < 1 {
				return fmt.Errorf("%w: rounds[%d].semantic_qa.concurrency must be >= 1", ErrExecutionPlanConfigInvalid, i)
			}
			// NOTE: fallback_shrink 当前仅 translate 轮支持缩批，semantic_qa 不暴露此字段。
			// 若未来需要，在此加 s.FallbackShrink ∈ [0,1] 校验（参考 translate 分支）。
			scope := s.SegmentScope
			if scope == "" {
				scope = "all"
			}
			switch scope {
			case "all", "with_issues", "with_issue_codes":
				// ok
			default:
				return fmt.Errorf("%w: rounds[%d].semantic_qa.segment_scope must be 'all', 'with_issues' or 'with_issue_codes'", ErrExecutionPlanConfigInvalid, i)
			}
			if scope == "with_issue_codes" && len(s.IssueCodes) == 0 {
				return fmt.Errorf("%w: rounds[%d].semantic_qa.issue_codes must contain at least one code when segment_scope is 'with_issue_codes'", ErrExecutionPlanConfigInvalid, i)
			}
			for _, code := range s.IssueCodes {
				if !qa.IsFilterableIssueCode(code) {
					return fmt.Errorf("%w: rounds[%d].semantic_qa.issue_codes contains invalid code %q", ErrExecutionPlanConfigInvalid, i, code)
				}
			}
		case "revise":
			if round.Revise == nil {
				return fmt.Errorf("%w: rounds[%d].revise config required when mode=revise", ErrExecutionPlanConfigInvalid, i)
			}
			r := round.Revise
			if r.BatchSize < 0 {
				return fmt.Errorf("%w: rounds[%d].revise.batch_size must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if r.MaxWordsPerBatch < 0 {
				return fmt.Errorf("%w: rounds[%d].revise.max_words_per_batch must be >= 0", ErrExecutionPlanConfigInvalid, i)
			}
			if r.BatchSize <= 0 && r.MaxWordsPerBatch <= 0 {
				return fmt.Errorf("%w: rounds[%d].revise.batch_size and max_words_per_batch cannot both be 0", ErrExecutionPlanConfigInvalid, i)
			}
			if r.Concurrency < 1 {
				return fmt.Errorf("%w: rounds[%d].revise.concurrency must be >= 1", ErrExecutionPlanConfigInvalid, i)
			}
			scope := r.SegmentScope
			if scope == "" {
				scope = "with_issues"
			}
			if scope != "with_issues" && scope != "with_issue_codes" {
				return fmt.Errorf("%w: rounds[%d].revise.segment_scope must be 'with_issues' or 'with_issue_codes'", ErrExecutionPlanConfigInvalid, i)
			}
			if scope == "with_issue_codes" && len(r.IssueCodes) == 0 {
				return fmt.Errorf("%w: rounds[%d].revise.issue_codes must contain at least one code when segment_scope is 'with_issue_codes'", ErrExecutionPlanConfigInvalid, i)
			}
			for _, code := range r.IssueCodes {
				if !qa.IsSemanticQACode(code) {
					return fmt.Errorf("%w: rounds[%d].revise.issue_codes contains invalid code %q", ErrExecutionPlanConfigInvalid, i, code)
				}
			}
			// NOTE: fallback_shrink 当前仅 translate 轮支持缩批，revise 不暴露此字段。
			// 若未来需要，在此加 r.FallbackShrink ∈ [0,1] 校验（参考 translate 分支）。
		case "correct":
			if round.Correct == nil {
				return fmt.Errorf("%w: rounds[%d].correct config required when mode=correct", ErrExecutionPlanConfigInvalid, i)
			}
			c := round.Correct
			if c.Concurrency < 1 {
				return fmt.Errorf("%w: rounds[%d].correct.concurrency must be >= 1", ErrExecutionPlanConfigInvalid, i)
			}
			// backend_id 不校验（correct 可省略）。
			if len(c.Rules) == 0 {
				return fmt.Errorf("%w: rounds[%d].correct.rules must contain at least one rule", ErrExecutionPlanConfigInvalid, i)
			}
			allowed := make(map[string]struct{})
			for _, name := range correct.AllRuleNames() {
				allowed[name] = struct{}{}
			}
			for _, r := range c.Rules {
				if _, ok := allowed[r.Name]; !ok {
					return fmt.Errorf("%w: rounds[%d].correct.rules contains invalid rule name %q (allowed: %v)", ErrExecutionPlanConfigInvalid, i, r.Name, correct.AllRuleNames())
				}
			}
			// NOTE: correct 不校验 batch_size/max_words_per_batch/retry（无批量、无外部 I/O、无重试语义）；
			// 后端 backend_id optional 化后，其余 mode 的 BackendID!=0 已由顶部 if 保证。
		default:
			return fmt.Errorf("%w: rounds[%d].mode must be 'translate', 'extract', 'adjudicate', 'semantic_qa', 'revise' or 'correct'", ErrExecutionPlanConfigInvalid, i)
		}
	}
	return nil
}
