package v013

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

// frozenExecutionTarget pins the historical target's root JSON fields. In
// particular, current response_format_version must not enter a schema 1 result.
// Nested source-to-target fields are additionally pinned by the full JSON golden.
// This is an offline importer only; it grants no permission to execute old specs.
type frozenExecutionTarget struct {
	SchemaVersion            int                                       `json:"schema_version"`
	DefaultsVersion          int                                       `json:"defaults_version"`
	Sources                  []execution.AssetSource                   `json:"sources"`
	RubyTemplates            execution.RubyTemplates                   `json:"ruby_templates"`
	RetryReminderTemplate    string                                    `json:"retry_reminder_template"`
	ExecutionPlanID          int                                       `json:"execution_plan_id"`
	ExecutionPlanName        string                                    `json:"execution_plan_name"`
	Strategy                 execution.StrategySnapshot                `json:"strategy"`
	Rounds                   []execution.JobRoundSnapshot              `json:"rounds"`
	SourceLang               string                                    `json:"source_lang"`
	TargetLang               string                                    `json:"target_lang"`
	GlossaryEnabled          bool                                      `json:"glossary_enabled"`
	TMEnabled                bool                                      `json:"tm_enabled,omitempty"`
	AutoApprove              bool                                      `json:"auto_approve,omitempty"`
	ExplicitSegmentSelection bool                                      `json:"explicit_segment_selection,omitempty"`
	RubyRetry                *execution.ExecutionPlanRubyRetrySnapshot `json:"ruby_retry,omitempty"`
}

func marshalFrozenExecution(s *execution.JobExecutionSnapshot) ([]byte, error) {
	return json.Marshal(frozenExecutionTarget{
		SchemaVersion: s.SchemaVersion, DefaultsVersion: s.DefaultsVersion,
		Sources: s.Sources, RubyTemplates: s.RubyTemplates,
		RetryReminderTemplate: s.RetryReminderTemplate,
		ExecutionPlanID:       s.ExecutionPlanID, ExecutionPlanName: s.ExecutionPlanName,
		Strategy: s.Strategy, Rounds: s.Rounds,
		SourceLang: s.SourceLang, TargetLang: s.TargetLang,
		GlossaryEnabled: s.GlossaryEnabled, TMEnabled: s.TMEnabled,
		AutoApprove: s.AutoApprove, ExplicitSegmentSelection: s.ExplicitSegmentSelection,
		RubyRetry: s.RubyRetry,
	})
}

// These checks freeze the target-1 domain constraints that the importer used
// before execution.ValidateSpec acquired the current-execution format gate.
// They never invoke a current resolver, choose defaults, or enable old execution.
func validateFrozenExecution(s *execution.JobExecutionSnapshot) error {
	if s == nil || s.SchemaVersion != targetSchemaVersion || s.DefaultsVersion != targetDefaultsVersion ||
		s.ResponseFormatVersion != 0 || s.RubyProtocolVersion != 0 || s.RubyValidatorVersion != 0 || s.RubyBatchProtocolVersion != 0 {
		return errors.New("unsupported frozen historical execution format")
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
	if err := validateFrozenProfile(execution.ProfileSpec{SchemaVersion: targetSchemaVersion, Protect: s.Strategy.Protect, Postprocess: s.Strategy.Postprocess, Repair: s.Strategy.Repair, Context: s.Strategy.Context, Ruby: s.Strategy.Ruby, QA: s.Strategy.QA}); err != nil {
		return err
	}
	for i, r := range s.Rounds {
		var batch, words, concurrency int
		var body string
		var retry execution.RetryConfig
		switch r.Mode {
		case "translate":
			if r.Translate == nil {
				return fmt.Errorf("round[%d] missing translate configuration", i)
			}
			t := r.Translate
			// The historical source has no per-round inline extraction setting.
			if t.InlineTermExtraction != nil {
				return fmt.Errorf("round[%d] unexpected current inline extraction configuration", i)
			}
			batch, words, concurrency, body, retry = t.BatchSize, t.MaxWordsPerBatch, t.Concurrency, t.Prompt.Content, t.Retry
			if t.FallbackShrink <= 0 || t.FallbackShrink > 1 {
				return fmt.Errorf("round[%d] invalid fallback shrink", i)
			}
			if t.SegmentFilter == nil || !slices.Contains([]string{"pending_only", "skip_approved", "all"}, t.SegmentFilter.StatusFilter) {
				return fmt.Errorf("round[%d] invalid segment filter", i)
			}
		case "extract":
			if r.Extract == nil {
				return fmt.Errorf("round[%d] missing extract configuration", i)
			}
			t := r.Extract
			batch, words, concurrency, body, retry = t.BatchSize, t.MaxWordsPerBatch, t.Concurrency, t.TemplateContent, t.Retry
		case "adjudicate":
			if r.Adjudicate == nil {
				return fmt.Errorf("round[%d] missing adjudicate configuration", i)
			}
			t := r.Adjudicate
			if t.AdjudicateCodes == nil {
				return fmt.Errorf("round[%d] missing frozen adjudication codes", i)
			}
			for _, code := range t.AdjudicateCodes {
				if !slices.Contains([]string{"untranslated", "source_residual", "length_ratio", "punctuation_surplus"}, code) {
					return fmt.Errorf("round[%d] invalid adjudication code", i)
				}
			}
			batch, words, concurrency, body, retry = t.BatchSize, t.MaxWordsPerBatch, t.Concurrency, t.TemplateContent, t.Retry
		case "semantic_qa":
			if r.SemanticQA == nil {
				return fmt.Errorf("round[%d] missing semantic_qa configuration", i)
			}
			t := r.SemanticQA
			if !slices.Contains([]string{"all", "with_issues", "with_issue_codes"}, t.SegmentScope) || t.IssueCodes == nil || (t.SegmentScope == "with_issue_codes" && len(t.IssueCodes) == 0) {
				return fmt.Errorf("round[%d] invalid or missing semantic QA scope/codes", i)
			}
			for _, code := range t.IssueCodes {
				if !frozenFilterableCode(code) {
					return fmt.Errorf("round[%d] invalid semantic QA filter code", i)
				}
			}
			batch, words, concurrency, body, retry = t.BatchSize, t.MaxWordsPerBatch, t.Concurrency, t.TemplateContent, t.Retry
		case "revise":
			if r.Revise == nil {
				return fmt.Errorf("round[%d] missing revise configuration", i)
			}
			t := r.Revise
			if !slices.Contains([]string{"with_issues", "with_issue_codes"}, t.SegmentScope) || t.IssueCodes == nil || (t.SegmentScope == "with_issue_codes" && len(t.IssueCodes) == 0) {
				return fmt.Errorf("round[%d] invalid or missing revision scope/codes", i)
			}
			for _, code := range t.IssueCodes {
				if !slices.Contains(semanticCodes(), code) {
					return fmt.Errorf("round[%d] invalid revision issue code", i)
				}
			}
			batch, words, concurrency, body, retry = t.BatchSize, t.MaxWordsPerBatch, t.Concurrency, t.TemplateContent, t.Retry
		case "correct":
			if r.Correct == nil || r.Correct.Concurrency < 1 {
				return fmt.Errorf("round[%d] invalid correct configuration", i)
			}
			continue
		default:
			return fmt.Errorf("round[%d] unsupported mode", i)
		}
		if batch < 0 || words < 0 || (r.Mode != "extract" && batch == 0 && words == 0) {
			return fmt.Errorf("round[%d] invalid batch limits", i)
		}
		if concurrency < 1 || strings.TrimSpace(body) == "" || retry.MaxAttempts < 0 || retry.BackoffMs < 0 {
			return fmt.Errorf("round[%d] invalid concurrency, template or retry configuration", i)
		}
		if err := validateFrozenBackend(r.Backend); err != nil {
			return fmt.Errorf("round[%d]: %w", i, err)
		}
	}
	if s.RubyRetry != nil && s.RubyRetry.Enabled {
		if s.RubyRetry.MaxAttempts < 1 {
			return errors.New("ruby retry attempts must be positive")
		}
		if err := validateFrozenBackend(s.RubyRetry.Backend); err != nil {
			return err
		}
	}
	if s.Strategy.Ruby.Enabled && (s.RubyTemplates.JSON == "" || s.RubyTemplates.Text == "") {
		return errors.New("missing frozen Ruby alignment templates")
	}
	return nil
}

func frozenCheckerCode(code string) bool {
	return slices.Contains([]string{"untranslated", "length_ratio", "duplicate", "source_residual", "punctuation_pairing", "punctuation_missing", "punctuation_surplus", "punctuation_wrap_loss", "whitespace_irregular", "repeated_space", "width_mix", "script_mismatch", "number_mismatch", "url_email_mismatch", "subtitle_line_count", "forbidden_term", "term_inconsistency", "leftover_placeholder", "xml_tag_mismatch", "duplicate_source_divergence"}, code)
}

func frozenFilterableCode(code string) bool {
	return frozenCheckerCode(code) || slices.Contains(semanticCodes(), code) || code == "ruby_restore_incomplete" || code == "ruby_tag_loss"
}

func validateFrozenProfile(p execution.ProfileSpec) error {
	if p.SchemaVersion != targetSchemaVersion {
		return errors.New("unsupported historical profile schema version")
	}
	for _, code := range p.QA.Checks {
		if !frozenCheckerCode(code) {
			return errors.New("unsupported QA checker name")
		}
	}
	if p.Context.Before < 0 || p.Context.After < 0 || p.Context.MaxChars < 0 {
		return errors.New("context window values cannot be negative")
	}
	for _, kind := range p.Ruby.PreserveKinds {
		if !slices.Contains([]string{"phonetic", "semantic", "creative"}, kind) {
			return errors.New("invalid ruby preserve kind")
		}
	}
	for _, rule := range p.Protect.Rules {
		if !slices.Contains([]string{"code", "link", "placeholder", "xml"}, rule) {
			return errors.New("unsupported protection rule")
		}
	}
	if p.QA.Enabled {
		if p.QA.LengthRatioMin < 0 || p.QA.LengthRatioMax < 0 || (p.QA.LengthRatioMin > 0 && p.QA.LengthRatioMax > 0 && p.QA.LengthRatioMin > p.QA.LengthRatioMax) {
			return errors.New("invalid QA ratios")
		}
		if p.QA.LengthMethod != "" && p.QA.LengthMethod != "char_weight" && p.QA.LengthMethod != "word_count" {
			return errors.New("invalid QA length method")
		}
	}
	return nil
}

func validateFrozenBackend(b execution.BackendSnapshot) error {
	if b.ID <= 0 || !b.Credential.Valid() {
		return errors.New("missing backend identity or credential binding")
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
		if level, ok := b.Options["thinking_level"].(string); ok && level != "off" {
			if _, ok := b.Options["thinking_budget_tokens"]; !ok {
				return errors.New("missing frozen thinking budget")
			}
		}
	}
	// Rechecking the importer-owned options can only verify existing fields;
	// a missing required field above is never repaired by its historical defaults.
	options, err := freezeBackendOptions(b.Type, b.Options)
	if err != nil {
		return err
	}
	if b.Options["base_url"] != options["base_url"] {
		return errors.New("backend endpoint must be frozen and normalized")
	}
	maxTokens, _ := frozenNumber(b.Options["max_tokens"])
	if b.Type != "openai" && maxTokens == 0 {
		return errors.New("backend max_tokens must be positive")
	}
	for _, key := range []string{"stream", "enable_prompt_cache"} {
		if value, exists := b.Options[key]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("backend %s must be boolean", key)
			}
		}
	}
	if value, ok := b.Options["response_format"].(string); !ok || !slices.Contains([]string{"json_schema", "json_object", "text", "none"}, value) {
		return errors.New("invalid response format")
	}
	for _, key := range []string{"temperature", "top_p"} {
		if value, exists := b.Options[key]; exists {
			n, ok := frozenNumber(value)
			limit := 1.0
			if key == "temperature" && b.Type != "anthropic" {
				limit = 2
			}
			if !ok || n < 0 || n > limit {
				return fmt.Errorf("invalid backend %s", key)
			}
		}
	}
	if value, exists := b.Options["thinking_level"]; exists {
		level, ok := value.(string)
		if !ok || !slices.Contains([]string{"off", "minimal", "low", "medium", "high"}, level) {
			return errors.New("invalid thinking level")
		}
		if b.Type == "anthropic" && level != "off" {
			budget, ok := frozenNumber(b.Options["thinking_budget_tokens"])
			if !ok || math.Trunc(budget) != budget || budget < 1024 || budget >= maxTokens {
				return errors.New("thinking budget must be at least 1024 and less than max_tokens")
			}
		}
	}
	return nil
}

func frozenNumber(value any) (float64, bool) {
	var n float64
	switch value := value.(type) {
	case int:
		n = float64(value)
	case int64:
		n = float64(value)
	case float64:
		n = value
	case json.Number:
		var err error
		n, err = value.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}
