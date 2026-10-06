package v013

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

// These assets and defaults belong to the source release, not the running
// service. See SOURCE.md. Never replace them with current template imports.
var (
	//go:embed assets/adjudication.tmpl
	adjudicationTemplate string
	//go:embed assets/semantic_qa.tmpl
	semanticQATemplate string
	//go:embed assets/revise.tmpl
	reviseTemplate string
	//go:embed assets/ruby_json.tmpl
	rubyJSONTemplate string
	//go:embed assets/ruby_text.tmpl
	rubyTextTemplate string
	//go:embed assets/retry_reminder.tmpl
	retryReminderTemplate string
)

func decodeSource(raw []byte, target any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("legacy execution data must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		// JSON errors can contain secret values or sensitive unknown field names.
		return errors.New("invalid or unsupported legacy execution fields")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("legacy execution data contains trailing values")
	}
	return nil
}

// Go raw strings discard CR bytes, and the source template loaders trimmed
// trailing newlines. Preserve those bytes even after a Windows Git checkout.
func sourceTemplate(raw string) string {
	return strings.TrimRight(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
}

func transcode(source, target any) error {
	raw, err := json.Marshal(source)
	if err != nil {
		return errors.New("cannot encode legacy execution data")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errors.New("cannot map legacy execution to target format")
	}
	return nil
}

func convertProfile(raw []byte) (execution.ProfileSpec, error) {
	var old sourceExecutionProfileConfigData
	var out execution.ProfileSpec
	if err := decodeSource(raw, &old); err != nil {
		return out, err
	}
	// v0.13.0 normalized these fields whenever loading an execution profile.
	if old.Context.Before < 1 {
		old.Context.Before = 1
	}
	if old.Context.After < 1 {
		old.Context.After = 1
	}
	if !old.Context.Enabled && old.Context.MaxChars == 0 {
		old.Context.Enabled = true
	}
	if old.Ruby.PreserveKinds == nil {
		old.Ruby.PreserveKinds = []string{"phonetic", "semantic", "creative"}
	}
	if err := transcode(old, &out); err != nil {
		return out, err
	}
	out.SchemaVersion = targetSchemaVersion
	return out, execution.ValidateProfile(out)
}

func semanticCodes() []string {
	return []string{"calque", "term_fidelity", "naturalness", "mistranslation", "omission", "addition", "grammar", "register"}
}

func freezeExecution(out execution.JobExecutionSnapshot) (*execution.ResolvedExecutionSpec, error) {
	out.SchemaVersion, out.DefaultsVersion = targetSchemaVersion, targetDefaultsVersion
	out.RetryReminderTemplate = sourceTemplate(retryReminderTemplate)
	if out.Strategy.QA.Checks == nil {
		out.Strategy.QA.Checks = []string{"untranslated", "length_ratio", "duplicate", "source_residual", "punctuation_pairing", "punctuation_missing", "punctuation_surplus", "punctuation_wrap_loss", "whitespace_irregular", "repeated_space", "width_mix", "script_mismatch", "number_mismatch", "url_email_mismatch", "subtitle_line_count", "forbidden_term", "term_inconsistency", "leftover_placeholder", "xml_tag_mismatch", "duplicate_source_divergence"}
	}
	if out.Strategy.QA.LengthMethod == "" {
		out.Strategy.QA.LengthMethod = "char_weight"
	}
	for i := range out.Rounds {
		round := &out.Rounds[i]
		switch round.Mode {
		case "extract":
			out.GlossaryEnabled = true
		case "adjudicate":
			if round.Adjudicate != nil && len(round.Adjudicate.AdjudicateCodes) == 0 {
				round.Adjudicate.AdjudicateCodes = []string{"source_residual", "punctuation_surplus"}
			}
		case "semantic_qa":
			if round.SemanticQA != nil {
				if round.SemanticQA.SegmentScope == "" {
					round.SemanticQA.SegmentScope = "all"
				}
				if round.SemanticQA.IssueCodes == nil {
					round.SemanticQA.IssueCodes = []string{}
				}
			}
		case "revise":
			if round.Revise != nil {
				if round.Revise.SegmentScope == "" {
					round.Revise.SegmentScope = "with_issues"
				}
				if len(round.Revise.IssueCodes) == 0 {
					round.Revise.IssueCodes = semanticCodes()
				}
			}
		}
	}
	profileID := 0
	if out.Strategy.ProfileID != nil {
		profileID = *out.Strategy.ProfileID
	}
	out.Sources = []execution.AssetSource{frozenSource("profile", strconv.Itoa(profileID), out.Strategy)}
	for i, round := range out.Rounds {
		out.Sources = append(out.Sources, frozenSource("round", strconv.Itoa(i), round))
	}
	if err := execution.ValidateSpec(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func frozenSource(kind, id string, data any) execution.AssetSource {
	raw, _ := json.Marshal(data)
	digest := sha256.Sum256(raw)
	return execution.AssetSource{Kind: kind, ID: id, Digest: hex.EncodeToString(digest[:])}
}

// Materialize only defaults that the v0.13.0 provider adapters supplied. The
// current target validator verifies the completed result without choosing them.
func freezeBackendOptions(provider string, input map[string]any) (map[string]any, error) {
	if !supportedProvider(provider) {
		return nil, errors.New("unsupported legacy provider")
	}
	opts := cloneOptions(input)
	setDefault := func(key string, value any) {
		if _, exists := opts[key]; !exists {
			opts[key] = value
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
	// v0.13.0 used Int64Opt here, not the current duration-string resolver.
	// Reject malformed old values rather than acquiring new execution semantics.
	for _, key := range []string{"max_tokens", "timeout"} {
		var n float64
		switch value := opts[key].(type) {
		case int:
			n = float64(value)
		case float64:
			n = value
		default:
			return nil, errors.New("legacy backend numeric options must be numbers")
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 0 {
			return nil, errors.New("legacy backend numeric options must be nonnegative integers")
		}
	}
	if opts["response_format"] == "" {
		opts["response_format"] = "json_schema"
	}
	endpoint, ok := opts["base_url"].(string)
	if _, exists := opts["base_url"]; exists && !ok {
		return nil, errors.New("legacy backend base_url must be a string")
	}
	endpoint, err := freezeEndpoint(provider, endpoint)
	if err != nil {
		return nil, err
	}
	opts["base_url"] = endpoint
	if level, ok := opts["thinking_level"].(string); ok && provider == "anthropic" && level != "off" {
		ratio, valid := map[string]float64{"minimal": 0.125, "low": 0.25, "medium": 0.5, "high": 0.75}[level]
		if !valid {
			return nil, errors.New("invalid legacy thinking level")
		}
		var tokens float64
		switch n := opts["max_tokens"].(type) {
		case int:
			tokens = float64(n)
		case float64:
			tokens = n
		}
		if tokens <= 1024 {
			return nil, errors.New("legacy thinking requires max_tokens above 1024")
		}
		budget := int64(tokens * ratio)
		if budget < 1024 {
			budget = 1024
		}
		if budget >= int64(tokens) {
			budget = int64(tokens) - 1
		}
		setDefault("thinking_budget_tokens", budget)
	}
	return opts, nil
}

func freezeEndpoint(provider, endpoint string) (string, error) {
	if strings.TrimSpace(endpoint) == "" {
		switch provider {
		case "openai":
			endpoint = "https://api.openai.com/v1"
		case "anthropic":
			endpoint = "https://api.anthropic.com"
		case "google":
			endpoint = "https://generativelanguage.googleapis.com"
		}
	}
	return credential.NormalizeEndpoint(provider, endpoint)
}
