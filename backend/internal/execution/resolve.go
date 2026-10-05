package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

const SchemaVersion = 1
const DefaultsVersion = 1

// Resolve 掌管应用层默认值。适配器负责提供已授权的资产、真实的模板内容
// 与稳定的凭据绑定。此处不发生任何 I/O。
func Resolve(in JobExecutionSnapshot) (*ResolvedExecutionSpec, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, errors.New("execution input cannot be serialized")
	}
	var out ResolvedExecutionSpec
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	out.SchemaVersion, out.DefaultsVersion = SchemaVersion, DefaultsVersion
	if out.RetryReminderTemplate == "" {
		out.RetryReminderTemplate = repair.DefaultRetryReminderTemplate
	}
	if out.SourceLang == "" {
		out.SourceLang = "auto"
	}
	if out.TargetLang == "" {
		out.TargetLang = "zh"
	}
	if out.Strategy.QA.Checks == nil {
		out.Strategy.QA.Checks = append(qa.AllCheckerNames(), qa.CodeDuplicateSourceDivergence)
	}
	if out.Strategy.QA.LengthMethod == "" {
		out.Strategy.QA.LengthMethod = "char_weight"
	}
	for i := range out.Rounds {
		r := &out.Rounds[i]
		resolveRoundSelection(r)
		if r.Mode == "correct" {
			continue
		}
		opts, err := ResolveBackendOptions(r.Backend.Type, r.Backend.Options)
		if err != nil {
			return nil, fmt.Errorf("round[%d]: %w", i, err)
		}
		r.Backend.Options = opts
		if r.Mode == "extract" {
			out.GlossaryEnabled = true
		}
	}
	if out.RubyRetry != nil && out.RubyRetry.Enabled {
		opts, err := ResolveBackendOptions(out.RubyRetry.Backend.Type, out.RubyRetry.Backend.Options)
		if err != nil {
			return nil, fmt.Errorf("ruby retry: %w", err)
		}
		out.RubyRetry.Backend.Options = opts
	}
	// 当授权入口收窄轮次时，重新计算派生来源；
	// 该入口提供的外部资产的来源信息保持不变。
	sources := out.Sources[:0]
	for _, item := range out.Sources {
		if item.Kind != "profile" && item.Kind != "round" {
			sources = append(sources, item)
		}
	}
	out.Sources = append(sources, source("profile", strconv.Itoa(ptrInt(out.Strategy.ProfileID)), out.Strategy))
	for i, round := range out.Rounds {
		out.Sources = append(out.Sources, source("round", strconv.Itoa(i), round))
	}
	if err := ValidateSpec(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// resolveRoundSelection 仅对新接受的执行输入运行。空列表是有意为之；
// nil 列表表示输入未提供该选择。
func resolveRoundSelection(r *JobRoundSnapshot) {
	switch r.Mode {
	case "adjudicate":
		if r.Adjudicate != nil && r.Adjudicate.AdjudicateCodes == nil {
			r.Adjudicate.AdjudicateCodes = qa.DefaultAdjudicateCodes()
		}
	case "semantic_qa":
		if r.SemanticQA != nil {
			if r.SemanticQA.SegmentScope == "" {
				r.SemanticQA.SegmentScope = "all"
			}
			if r.SemanticQA.IssueCodes == nil {
				r.SemanticQA.IssueCodes = []string{}
			}
		}
	case "revise":
		if r.Revise != nil {
			if r.Revise.SegmentScope == "" {
				r.Revise.SegmentScope = "with_issues"
			}
			if r.Revise.IssueCodes == nil {
				r.Revise.IssueCodes = []string{}
				if r.Revise.SegmentScope == "with_issues" {
					r.Revise.IssueCodes = qa.SemanticQACodes()
				}
			}
		}
	}
}

func ptrInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
func source(kind, id string, value any) AssetSource {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return AssetSource{Kind: kind, ID: id, Digest: hex.EncodeToString(digest[:])}
}

// ResolveBackendOptions 不填充上游有意留空的默认值，同时把我们
// provider 适配器当前采用的每个默认值实体化。
func ResolveBackendOptions(provider string, input map[string]any) (map[string]any, error) {
	if provider != "openai" && provider != "anthropic" && provider != "google" {
		return nil, errors.New("unsupported provider")
	}
	allowed := map[string]bool{"type": true, "model": true, "base_url": true, "max_tokens": true, "timeout": true, "response_format": true, "temperature": true, "top_p": true, "stream": true, "thinking_level": true, "enable_prompt_cache": true, "thinking_budget_tokens": true}
	opts := make(map[string]any, len(input)+6)
	for key, value := range input {
		if !allowed[key] {
			return nil, errors.New("backend options contains an unsupported or sensitive field")
		}
		if value == nil {
			return nil, fmt.Errorf("backend option %s cannot be null", key)
		}
		opts[key] = value
	}
	model, ok := opts["model"].(string)
	if !ok || strings.TrimSpace(model) == "" {
		return nil, errors.New("backend model is required")
	}
	setDefault := func(k string, v any) {
		if _, ok := opts[k]; !ok {
			opts[k] = v
		}
	}
	maxTokens := 0
	if provider != "openai" {
		maxTokens = 8192
	}
	setDefault("max_tokens", maxTokens)
	setDefault("timeout", 60)
	setDefault("response_format", "json_schema")
	setDefault("stream", false)
	if provider == "anthropic" {
		setDefault("enable_prompt_cache", true)
	}
	ep, _ := opts["base_url"].(string)
	if value, exists := opts["base_url"]; exists {
		if _, ok := value.(string); !ok {
			return nil, errors.New("backend base_url must be a string")
		}
	}
	ep, err := credential.NormalizeEndpoint(provider, ep)
	if err != nil {
		return nil, err
	}
	opts["base_url"] = ep
	if raw, ok := opts["timeout"].(string); ok {
		duration, err := time.ParseDuration(raw)
		if err != nil || duration < 0 {
			return nil, errors.New("backend timeout must be a nonnegative duration")
		}
		opts["timeout"] = duration.String()
	}
	for _, key := range []string{"max_tokens", "timeout"} {
		if key == "timeout" {
			if _, ok := opts[key].(string); ok {
				continue
			}
		}
		n, ok := number(opts[key])
		if !ok || n < 0 || math.Trunc(n) != n {
			return nil, fmt.Errorf("backend %s must be a nonnegative integer", key)
		}
		if key == "max_tokens" && provider != "openai" && n == 0 {
			return nil, errors.New("backend max_tokens must be positive")
		}
	}
	for _, key := range []string{"stream", "enable_prompt_cache"} {
		if v, exists := opts[key]; exists {
			if _, ok := v.(bool); !ok {
				return nil, fmt.Errorf("backend %s must be boolean", key)
			}
		}
	}
	if rf, ok := opts["response_format"].(string); !ok || (rf != "json_schema" && rf != "json_object" && rf != "text" && rf != "none") {
		return nil, errors.New("invalid response format")
	}
	for _, key := range []string{"temperature", "top_p"} {
		if v, exists := opts[key]; exists {
			n, ok := number(v)
			max := 1.0
			if key == "temperature" && provider != "anthropic" {
				max = 2
			}
			if !ok || n < 0 || n > max {
				return nil, fmt.Errorf("invalid backend %s", key)
			}
		}
	}
	if value, exists := opts["thinking_level"]; exists {
		level, ok := value.(string)
		if !ok {
			return nil, errors.New("invalid thinking level")
		}
		ratios := map[string]float64{"off": 0, "minimal": 0.125, "low": 0.25, "medium": 0.5, "high": 0.75}
		ratio, ok := ratios[level]
		if !ok {
			return nil, errors.New("invalid thinking level")
		}
		if provider == "anthropic" && level != "off" {
			n, _ := number(opts["max_tokens"])
			budget := int64(n * ratio)
			if budget < 1024 {
				budget = 1024
			}
			setDefault("thinking_budget_tokens", budget)
			fixed, ok := number(opts["thinking_budget_tokens"])
			if !ok || math.Trunc(fixed) != fixed || fixed < 1024 || fixed >= n {
				return nil, errors.New("thinking budget must be at least 1024 and less than max_tokens")
			}
		}
	}
	return opts, nil
}

func number(v any) (float64, bool) {
	var n float64
	switch x := v.(type) {
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case float64:
		n = x
	case json.Number:
		var err error
		n, err = x.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}

// ValidateSpec 绝不填充默认值。恢复不完整的快照会直接失败。
func ValidateSpec(s *ResolvedExecutionSpec) error {
	if s == nil || s.SchemaVersion != SchemaVersion || s.DefaultsVersion != DefaultsVersion {
		return errors.New("unsupported or missing execution snapshot version")
	}
	if s.Strategy.QA.Checks == nil || s.Strategy.QA.LengthMethod == "" {
		return errors.New("missing frozen QA checks or length method")
	}
	if s.Strategy.Repair.Enabled && s.Strategy.Repair.PromptUpgrade && s.RetryReminderTemplate == "" {
		return errors.New("missing frozen retry reminder template")
	}
	if s.SourceLang == "" || s.TargetLang == "" || len(s.Rounds) == 0 {
		return errors.New("execution requires languages and rounds")
	}
	if s.Strategy.Context.Before < 0 || s.Strategy.Context.After < 0 || s.Strategy.Context.MaxChars < 0 {
		return errors.New("invalid context window")
	}
	if err := ValidateProfile(ProfileSpec{SchemaVersion: SchemaVersion, Protect: s.Strategy.Protect, Postprocess: s.Strategy.Postprocess, Repair: s.Strategy.Repair, Glossary: s.Strategy.Glossary, Context: s.Strategy.Context, Ruby: s.Strategy.Ruby, QA: s.Strategy.QA}); err != nil {
		return err
	}
	for i, r := range s.Rounds {
		var batch, words, concurrency int
		var body string
		var retry RetryConfig
		switch r.Mode {
		case "translate":
			if r.Translate == nil {
				return fmt.Errorf("round[%d] missing translate configuration", i)
			}
			t := r.Translate
			batch, concurrency, body, retry = t.BatchSize, t.Concurrency, t.Prompt.Content, t.Retry
			words = t.MaxWordsPerBatch
			if t.FallbackShrink <= 0 || t.FallbackShrink > 1 {
				return fmt.Errorf("round[%d] invalid fallback shrink", i)
			}
			if t.SegmentFilter == nil || (t.SegmentFilter.StatusFilter != "pending_only" && t.SegmentFilter.StatusFilter != "skip_approved" && t.SegmentFilter.StatusFilter != "all") {
				return fmt.Errorf("round[%d] invalid segment filter", i)
			}
		case "extract":
			if r.Extract == nil {
				return fmt.Errorf("round[%d] missing extract configuration", i)
			}
			t := r.Extract
			batch, concurrency, body, retry = t.BatchSize, t.Concurrency, t.TemplateContent, t.Retry
			words = t.MaxWordsPerBatch
		case "adjudicate":
			if r.Adjudicate == nil {
				return fmt.Errorf("round[%d] missing adjudicate configuration", i)
			}
			t := r.Adjudicate
			if t.AdjudicateCodes == nil {
				return fmt.Errorf("round[%d] missing frozen adjudication codes", i)
			}
			for _, code := range t.AdjudicateCodes {
				if !qa.IsAdjudicableCode(code) {
					return fmt.Errorf("round[%d] invalid adjudication code", i)
				}
			}
			batch, concurrency, body, retry = t.BatchSize, t.Concurrency, t.TemplateContent, t.Retry
			words = t.MaxWordsPerBatch
		case "semantic_qa":
			if r.SemanticQA == nil {
				return fmt.Errorf("round[%d] missing semantic_qa configuration", i)
			}
			t := r.SemanticQA
			if t.SegmentScope != "all" && t.SegmentScope != "with_issues" && t.SegmentScope != "with_issue_codes" {
				return fmt.Errorf("round[%d] invalid or missing semantic QA scope", i)
			}
			if t.IssueCodes == nil || (t.SegmentScope == "with_issue_codes" && len(t.IssueCodes) == 0) {
				return fmt.Errorf("round[%d] missing frozen semantic QA codes", i)
			}
			for _, code := range t.IssueCodes {
				if !qa.IsFilterableIssueCode(code) {
					return fmt.Errorf("round[%d] invalid semantic QA filter code", i)
				}
			}
			batch, concurrency, body, retry = t.BatchSize, t.Concurrency, t.TemplateContent, t.Retry
			words = t.MaxWordsPerBatch
		case "revise":
			if r.Revise == nil {
				return fmt.Errorf("round[%d] missing revise configuration", i)
			}
			t := r.Revise
			if t.SegmentScope != "with_issues" && t.SegmentScope != "with_issue_codes" {
				return fmt.Errorf("round[%d] invalid or missing revision scope", i)
			}
			if t.IssueCodes == nil || (t.SegmentScope == "with_issue_codes" && len(t.IssueCodes) == 0) {
				return fmt.Errorf("round[%d] missing frozen revision codes", i)
			}
			for _, code := range t.IssueCodes {
				if !qa.IsSemanticQACode(code) {
					return fmt.Errorf("round[%d] invalid revision issue code", i)
				}
			}
			batch, concurrency, body, retry = t.BatchSize, t.Concurrency, t.TemplateContent, t.Retry
			words = t.MaxWordsPerBatch
		case "correct":
			if r.Correct == nil || r.Correct.Concurrency < 1 {
				return fmt.Errorf("round[%d] invalid correct configuration", i)
			}
			continue
		default:
			return fmt.Errorf("round[%d] unsupported mode", i)
		}
		if err := ValidateBatchLimits(r.Mode, batch, words); err != nil {
			return fmt.Errorf("round[%d].%s.%w", i, r.Mode, err)
		}
		if concurrency < 1 {
			return fmt.Errorf("round[%d].%s.concurrency must be >= 1", i, r.Mode)
		}
		if strings.TrimSpace(body) == "" {
			return fmt.Errorf("round[%d].%s missing template content", i, r.Mode)
		}
		if retry.MaxAttempts < 0 || retry.BackoffMs < 0 {
			return fmt.Errorf("round[%d].%s.retry max_attempts and backoff_ms must be >= 0", i, r.Mode)
		}
		if err := validateBackend(r.Backend); err != nil {
			return fmt.Errorf("round[%d]: %w", i, err)
		}
	}
	if s.RubyRetry != nil && s.RubyRetry.Enabled {
		if s.RubyRetry.MaxAttempts < 1 {
			return errors.New("ruby retry attempts must be positive")
		}
		if err := validateBackend(s.RubyRetry.Backend); err != nil {
			return err
		}
	}
	if s.Strategy.Ruby.Enabled && (s.RubyTemplates.JSON == "" || s.RubyTemplates.Text == "") {
		return errors.New("missing frozen Ruby alignment templates")
	}
	return nil
}

func validateBackend(b BackendSnapshot) error {
	if b.ID <= 0 {
		return errors.New("missing backend identity")
	}
	if !b.Credential.Valid() {
		return errors.New("missing credential binding")
	}
	for _, key := range []string{"max_tokens", "timeout", "response_format", "stream", "base_url"} {
		if _, ok := b.Options[key]; !ok {
			return fmt.Errorf("missing frozen backend option %s", key)
		}
	}
	if b.Type == "anthropic" {
		if _, ok := b.Options["enable_prompt_cache"]; !ok {
			return errors.New("missing frozen prompt cache setting")
		}
	}
	if level, ok := b.Options["thinking_level"].(string); ok && b.Type == "anthropic" && level != "off" {
		if _, ok := b.Options["thinking_budget_tokens"]; !ok {
			return errors.New("missing frozen thinking budget")
		}
	}
	opts, err := ResolveBackendOptions(b.Type, b.Options)
	if err != nil {
		return err
	}
	if b.Options["base_url"] != opts["base_url"] {
		return errors.New("backend endpoint must be frozen and normalized")
	}
	return nil
}
