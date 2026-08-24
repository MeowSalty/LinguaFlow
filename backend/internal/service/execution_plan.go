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
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
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
	client   *ent.Client
	users    *UserService
	profiles *ExecutionProfileService
}

// NewExecutionPlanService 创建执行计划模板服务。
func NewExecutionPlanService(client *ent.Client, users *UserService, profiles *ExecutionProfileService) *ExecutionPlanService {
	return &ExecutionPlanService{client: client, users: users, profiles: profiles}
}

// CreateExecutionPlanTemplateInput 创建执行计划模板的输入参数。
type CreateExecutionPlanTemplateInput struct {
	Name        string                              `json:"name"`
	Description string                              `json:"description"`
	Scope       string                              `json:"scope"` // user / org
	OwnerUserID *int                                `json:"owner_user_id,omitempty"`
	OwnerOrgID  *int                                `json:"owner_org_id,omitempty"`
	ProfileID   int                                 `json:"profile_id"` // 计划级策略引用（ExecutionProfile），为全管道供七项行为预设
	RubyRetry   schema.ExecutionPlanRubyRetryConfig `json:"ruby_retry"`
	Rounds      []schema.ExecutionRoundConfig       `json:"rounds"`
}

// UpdateExecutionPlanTemplateInput 更新执行计划模板的输入参数。
type UpdateExecutionPlanTemplateInput struct {
	Name        *string                              `json:"name,omitempty"`
	Description *string                              `json:"description,omitempty"`
	ProfileID   *int                                 `json:"profile_id,omitempty"` // nil = 保留现值
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

// Create 创建执行计划模板。userID 为创建者，用于计划级策略引用的属主校验。
func (s *ExecutionPlanService) Create(ctx context.Context, userID int, input CreateExecutionPlanTemplateInput) (*ent.ExecutionPlanTemplate, error) {
	if input.Scope == "" {
		input.Scope = ScopeUser
	}
	if input.Scope != ScopeUser && input.Scope != ScopeOrg {
		return nil, ErrExecutionPlanScopeInvalid
	}

	// 计划级 profile_id 引用校验（格式 + 存在性 + 属主；策略引用已从 translate 轮级上提到计划级）。
	if err := s.validatePlanProfileRef(ctx, userID, input.ProfileID); err != nil {
		return nil, err
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
		SetProfileID(input.ProfileID).
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

	// 计划级 profile_id 引用校验（格式 + 存在性 + 属主）；nil 表示保留现值（随 RubyRetry 惯例）。
	if input.ProfileID != nil {
		if err := s.validatePlanProfileRef(ctx, userID, *input.ProfileID); err != nil {
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
	if input.ProfileID != nil {
		update.SetProfileID(*input.ProfileID)
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
// 如果有 Job 引用了该模板，拒绝删除。
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

// validatePlanProfileID 校验计划级 profile_id 引用的格式：非零；负数必须可解析为
// 内置策略（IsBuiltinID 仅判定负号，可解析集合以 BuiltinExecutionProfile 为准，
// 当前仅 -1）。正数的存在性与属主校验需查库，见 validatePlanProfileRef。
func validatePlanProfileID(profileID int) error {
	if profileID == 0 {
		return fmt.Errorf("%w: profile_id must not be zero", ErrExecutionPlanConfigInvalid)
	}
	if profileID < 0 && templates.BuiltinExecutionProfile(profileID) == nil {
		return fmt.Errorf("%w: profile_id %d is not a valid builtin template", ErrExecutionPlanConfigInvalid, profileID)
	}
	return nil
}

// validatePlanProfileRef 校验计划级 profile_id 引用对调用者可用：负数须可解析为
// 内置策略；正数经 GetByID 确认存在、CheckAccess 复核属主/成员资格（规则单一来源
// 见 ExecutionProfileService.CheckAccess）。越权或不存在的引用一律按 not found
// 报错，不泄露他人资源的存在性（与 checkAccess 同一惯例）。
func (s *ExecutionPlanService) validatePlanProfileRef(ctx context.Context, userID, profileID int) error {
	if err := validatePlanProfileID(profileID); err != nil {
		return err
	}
	if profileID < 0 {
		return nil // 内置策略：validatePlanProfileID 已确认可解析
	}
	notFound := fmt.Errorf("%w: profile_id %d not found", ErrExecutionPlanConfigInvalid, profileID)
	profile, err := s.profiles.GetByID(ctx, profileID)
	if errors.Is(err, ErrExecutionProfileNotFound) {
		return notFound
	}
	if err != nil {
		return err
	}
	if err := s.profiles.CheckAccess(ctx, userID, profile); err != nil {
		if errors.Is(err, ErrExecutionProfileNotFound) {
			return notFound
		}
		return err
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
				if code != "source_residual" && code != "length_ratio" {
					return fmt.Errorf("%w: rounds[%d].adjudicate.adjudicate_codes contains invalid code %q (allowed: source_residual, length_ratio)", ErrExecutionPlanConfigInvalid, i, code)
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
