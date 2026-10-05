package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
	"gopkg.in/yaml.v3"
)

type CLIConfigGlossary struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
	Save    bool   `yaml:"save"`
}

// CLIConfig 是翻译文档的输入，绝不是持久化的执行快照。
// 密钥会在 resolve 之前转移到进程本地注册表中。
type CLIConfig struct {
	Kind                     string                                 `yaml:"kind"`
	Version                  int                                    `yaml:"version"`
	SourceLang               string                                 `yaml:"source_lang"`
	TargetLang               string                                 `yaml:"target_lang"`
	Backends                 map[string]CLIConfigBackend            `yaml:"backends"`
	PromptTemplates          map[string]CLIConfigPromptTemplate     `yaml:"translation_prompt_templates"`
	BootstrapPromptTemplates map[string]CLIConfigBootstrapTemplate  `yaml:"bootstrap_prompt_templates"`
	TranslationProfiles      map[string]CLIConfigTranslationProfile `yaml:"translation_profiles"`
	Execution                CLIConfigExecution                     `yaml:"execution"`
	Glossary                 CLIConfigGlossary                      `yaml:"glossary"`
	TranslationMemory        TMConfig                               `yaml:"translation_memory"`
	Plugins                  PluginsConfig                          `yaml:"plugins"`
	Output                   OutputConfig                           `yaml:"output"`
	Log                      LogConfig                              `yaml:"log"`
}
type CLIConfigBackend struct {
	Type               string         `yaml:"type"`
	Enabled            bool           `yaml:"enabled"`
	RateLimitPerMinute int            `yaml:"rate_limit_per_minute"`
	Secret             string         `yaml:"secret"`
	Options            map[string]any `yaml:"options"`
}
type CLIConfigPromptTemplate struct {
	Content string `yaml:"content"`
	File    string `yaml:"file"`
}
type CLIConfigBootstrapTemplate = CLIConfigPromptTemplate
type CLIConfigTranslationProfile struct {
	execution.ProfileSpec `yaml:",inline"`
	File                  string `yaml:"file"`
}
type CLIConfigExecution struct {
	Profile   string              `yaml:"profile"`
	Rounds    []CLIConfigRound    `yaml:"rounds"`
	RubyRetry *CLIConfigRubyRetry `yaml:"ruby_retry,omitempty"`
}
type CLIConfigRubyRetry struct {
	Enabled     bool   `yaml:"enabled"`
	Backend     string `yaml:"backend"`
	MaxAttempts int    `yaml:"max_attempts"`
}
type CLIConfigRound struct {
	Mode      string                   `yaml:"mode"`
	Backend   string                   `yaml:"backend"`
	Translate *CLIConfigTranslateRound `yaml:"translate,omitempty"`
	Extract   *CLIConfigExtractRound   `yaml:"extract,omitempty"`
	Revise    *CLIConfigReviseRound    `yaml:"revise,omitempty"`
}
type CLIConfigTranslateRound struct {
	Prompt           string      `yaml:"prompt"`
	BatchSize        int         `yaml:"batch_size"`
	MaxWordsPerBatch int         `yaml:"max_words_per_batch"`
	Concurrency      int         `yaml:"concurrency"`
	FallbackShrink   float64     `yaml:"fallback_shrink"`
	Retry            RetryConfig `yaml:"retry"`
}
type CLIConfigExtractRound struct {
	Template             string      `yaml:"template"`
	BatchSize            int         `yaml:"batch_size"`
	MaxWordsPerBatch     int         `yaml:"max_words_per_batch"`
	Concurrency          int         `yaml:"concurrency"`
	MaxTermsPer1000Chars float64     `yaml:"max_terms_per_1000_chars"`
	MinSourceLen         int         `yaml:"min_source_len"`
	Retry                RetryConfig `yaml:"retry"`
}
type CLIConfigReviseRound struct {
	BatchSize        int         `yaml:"batch_size"`
	MaxWordsPerBatch int         `yaml:"max_words_per_batch"`
	Concurrency      int         `yaml:"concurrency"`
	SegmentScope     string      `yaml:"segment_scope,omitempty"`
	IssueCodes       []string    `yaml:"issue_codes,omitempty"`
	Retry            RetryConfig `yaml:"retry"`
}

type CLIInputs struct {
	ConfigPath       *string
	Environment      map[string]string
	WorkingDirectory string
	SourceLang       *string
	TargetLang       *string
	LogLevel         *string
	LogFormat        *string
}

// LoadCLIConfig 是面向命令调用方的便捷边界。路径为空表示按环境选择或使用
// 内置配置，绝不会进行目录搜索。
func LoadCLIConfig(path string) (*CLIConfig, error) {
	in := CLIInputs{Environment: Environment()}
	if path != "" {
		in.ConfigPath = &path
	}
	return ResolveCLIConfig(in)
}

// ResolveCLIConfig 对内嵌文档与外部文档使用相同的解码器和引用读取器。
// 默认值在解码前按各类型输入对象逐一注入，以保留显式给出的 false、
// 零值与空列表。
func ResolveCLIConfig(in CLIInputs) (*CLIConfig, error) {
	cwd := in.WorkingDirectory
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	path, selected := in.Environment["LINGUAFLOW_TRANSLATION_CONFIG"]
	if in.ConfigPath != nil {
		path = *in.ConfigPath
		selected = true
	}
	var data []byte
	base := cwd
	var readReference func(string) ([]byte, error)
	if selected {
		if strings.TrimSpace(path) == "" {
			return nil, errors.New("translation config path must not be empty")
		}
		path = absolutePath(path, cwd)
		base = filepath.Dir(path)
		data, err = os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: translation document does not exist", ErrConfigNotFound)
		}
		if err != nil {
			return nil, errors.New("cannot read translation document")
		}
		readReference = func(ref string) ([]byte, error) { return readExternalFileBytes(ref, base) }
	} else {
		data = templates.DefaultConfigYAML()
		readReference = func(ref string) ([]byte, error) {
			ref = filepath.ToSlash(ref)
			if !fs.ValidPath(ref) {
				return nil, errors.New("reference must remain inside the configuration directory")
			}
			data, err := fs.ReadFile(templates.EmbeddedFS(), "default/"+ref)
			if err != nil {
				return nil, errors.New("cannot read embedded reference")
			}
			return data, nil
		}
	}
	root, err := parseTranslationYAML(data)
	if err != nil {
		return nil, err
	}
	kind, version := mappingValue(root, "kind"), mappingValue(root, "version")
	if kind == nil || kind.Tag != "!!str" || kind.Value != "translation" {
		return nil, errors.New("translation document requires kind: translation")
	}
	if version == nil || version.Tag != "!!int" || version.Value != "2" {
		return nil, errors.New("translation document requires version: 2")
	}
	if err := expandTranslationScalars(root, "", in.Environment); err != nil {
		return nil, err
	}
	cfg := &CLIConfig{SourceLang: "auto", TargetLang: "zh", Glossary: CLIConfigGlossary{Path: "glossary.csv", Save: true}, Log: LogConfig{Level: "info", Format: "text"}}
	if err := strictTranslationDecode(root, cfg, "translation"); err != nil {
		return nil, err
	}
	if profile := mappingValue(mappingValue(root, "execution"), "profile"); profile != nil && strings.TrimSpace(profile.Value) == "" {
		return nil, errors.New("execution.profile must not be empty when specified")
	}
	for name, b := range cfg.Backends {
		node := mappingValue(mappingValue(root, "backends"), name)
		if mappingValue(node, "enabled") == nil {
			b.Enabled = true
		}
		if b.Options == nil {
			b.Options = map[string]any{}
		}
		if _, exists := b.Options["api_key"]; exists {
			return nil, fmt.Errorf("backends[%s].options.api_key is unsupported; use secret", name)
		}
		cfg.Backends[name] = b
	}
	for name, p := range cfg.PromptTemplates {
		node := mappingValue(mappingValue(root, "translation_prompt_templates"), name)
		if mappingValue(node, "content") != nil && mappingValue(node, "file") != nil {
			return nil, fmt.Errorf("translation_prompt_templates[%s]: content and file are mutually exclusive", name)
		}
		p, err = resolveCLIPrompt(p, readReference)
		if err != nil {
			return nil, fmt.Errorf("translation_prompt_templates[%s]: %w", name, err)
		}
		cfg.PromptTemplates[name] = p
	}
	for name, p := range cfg.BootstrapPromptTemplates {
		node := mappingValue(mappingValue(root, "bootstrap_prompt_templates"), name)
		if mappingValue(node, "content") != nil && mappingValue(node, "file") != nil {
			return nil, fmt.Errorf("bootstrap_prompt_templates[%s]: content and file are mutually exclusive", name)
		}
		p, err = resolveCLIPrompt(p, readReference)
		if err != nil {
			return nil, fmt.Errorf("bootstrap_prompt_templates[%s]: %w", name, err)
		}
		cfg.BootstrapPromptTemplates[name] = p
	}
	for name, p := range cfg.TranslationProfiles {
		node := mappingValue(mappingValue(root, "translation_profiles"), name)
		if p.File != "" {
			if len(node.Content) != 2 {
				return nil, fmt.Errorf("translation_profiles[%s]: file and inline fields are mutually exclusive", name)
			}
			raw, err := readReference(p.File)
			if err != nil {
				return nil, fmt.Errorf("translation_profiles[%s].file: %w", name, err)
			}
			node, err = parseTranslationYAML(raw)
			if err != nil {
				return nil, fmt.Errorf("translation_profiles[%s].file: %w", name, err)
			}
			if err := expandTranslationScalars(node, "profile", in.Environment); err != nil {
				return nil, err
			}
		}
		version := mappingValue(node, "schema_version")
		if version == nil || version.Tag != "!!int" || version.Value != "1" {
			return nil, fmt.Errorf("translation_profiles[%s] requires schema_version: 1", name)
		}
		resolved := execution.DefaultProfile()
		if err := strictTranslationDecode(node, &resolved, "translation_profiles["+name+"]"); err != nil {
			return nil, err
		}
		cfg.TranslationProfiles[name] = CLIConfigTranslationProfile{ProfileSpec: resolved}
	}
	if cfg.TranslationProfiles == nil {
		cfg.TranslationProfiles = map[string]CLIConfigTranslationProfile{}
	}
	roundNodes := mappingValue(mappingValue(root, "execution"), "rounds")
	for i := range cfg.Execution.Rounds {
		r := &cfg.Execution.Rounds[i]
		node := roundNodes.Content[i]
		switch r.Mode {
		case "translate":
			if r.Translate == nil {
				return nil, fmt.Errorf("execution.rounds[%d] requires translate settings", i)
			}
			defaults := CLIConfigTranslateRound{BatchSize: 1, Concurrency: 4, FallbackShrink: 0.5, Retry: defaultCLIRetry()}
			if err := strictTranslationDecode(mappingValue(node, "translate"), &defaults, fmt.Sprintf("execution.rounds[%d].translate", i)); err != nil {
				return nil, err
			}
			if ref := mappingValue(mappingValue(node, "translate"), "prompt"); ref != nil && strings.TrimSpace(ref.Value) == "" {
				return nil, fmt.Errorf("execution.rounds[%d].translate.prompt must not be empty when specified", i)
			}
			r.Translate = &defaults
		case "extract":
			if r.Extract == nil {
				return nil, fmt.Errorf("execution.rounds[%d] requires extract settings", i)
			}
			defaults := CLIConfigExtractRound{BatchSize: 20, Concurrency: 2, MaxTermsPer1000Chars: 25, MinSourceLen: 2, Retry: defaultCLIRetry()}
			if err := strictTranslationDecode(mappingValue(node, "extract"), &defaults, fmt.Sprintf("execution.rounds[%d].extract", i)); err != nil {
				return nil, err
			}
			if ref := mappingValue(mappingValue(node, "extract"), "template"); ref != nil && strings.TrimSpace(ref.Value) == "" {
				return nil, fmt.Errorf("execution.rounds[%d].extract.template must not be empty when specified", i)
			}
			r.Extract = &defaults
		case "revise":
			if r.Revise == nil {
				return nil, fmt.Errorf("execution.rounds[%d] requires revise settings", i)
			}
			defaults := CLIConfigReviseRound{BatchSize: 10, Concurrency: 1, SegmentScope: "with_issues", Retry: defaultCLIRetry()}
			if err := strictTranslationDecode(mappingValue(node, "revise"), &defaults, fmt.Sprintf("execution.rounds[%d].revise", i)); err != nil {
				return nil, err
			}
			r.Revise = &defaults
		default:
			return nil, fmt.Errorf("execution.rounds[%d]: CLI supports translate, extract and revise modes", i)
		}
	}
	if cfg.Execution.RubyRetry != nil {
		ruby := CLIConfigRubyRetry{Enabled: true, MaxAttempts: 1}
		if err := strictTranslationDecode(mappingValue(mappingValue(root, "execution"), "ruby_retry"), &ruby, "execution.ruby_retry"); err != nil {
			return nil, err
		}
		cfg.Execution.RubyRetry = &ruby
	}
	if cfg.Glossary.Path != "" {
		cfg.Glossary.Path = absolutePath(cfg.Glossary.Path, base)
	}
	if in.SourceLang != nil {
		cfg.SourceLang = *in.SourceLang
	}
	if in.TargetLang != nil {
		cfg.TargetLang = *in.TargetLang
	}
	if in.LogLevel != nil {
		cfg.Log.Level = *in.LogLevel
	}
	if in.LogFormat != nil {
		cfg.Log.Format = *in.LogFormat
	}
	if err := ValidateCLIConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaultCLIRetry() RetryConfig { return RetryConfig{MaxAttempts: 3, BackoffMs: 2000, Jitter: true} }

func resolveCLIPrompt(p CLIConfigPromptTemplate, read func(string) ([]byte, error)) (CLIConfigPromptTemplate, error) {
	if p.Content != "" && p.File != "" {
		return p, errors.New("content and file are mutually exclusive")
	}
	if p.File != "" {
		data, err := read(p.File)
		if err != nil {
			return p, err
		}
		p.Content = string(data)
		p.File = ""
	}
	if p.Content == "" {
		return p, errors.New("template content must not be empty")
	}
	return p, nil
}

func ResolveExecutionProfile(cfg *CLIConfig) (execution.ProfileSpec, error) {
	p, ok := cfg.TranslationProfiles[cfg.Execution.Profile]
	if !ok && cfg.Execution.Profile == "" {
		return execution.DefaultProfile(), nil
	}
	if !ok {
		return execution.ProfileSpec{}, errors.New("execution.profile does not reference an existing profile")
	}
	return p.ProfileSpec, nil
}
func BuiltinExecutionProfile() execution.ProfileSpec { return execution.DefaultProfile() }

func ValidateCLIConfig(cfg *CLIConfig) error {
	if cfg.Kind != "translation" || cfg.Version != 2 {
		return errors.New("translation document requires kind: translation and version: 2")
	}
	if strings.TrimSpace(cfg.SourceLang) == "" || strings.TrimSpace(cfg.TargetLang) == "" {
		return errors.New("source_lang and target_lang must not be empty")
	}
	if len(cfg.Execution.Rounds) == 0 {
		return errors.New("execution.rounds must not be empty")
	}
	hasContentRound := false
	for i, r := range cfg.Execution.Rounds {
		var batch, words, concurrency int
		var retry RetryConfig
		count := 0
		if r.Translate != nil {
			count++
		}
		if r.Extract != nil {
			count++
		}
		if r.Revise != nil {
			count++
		}
		if count != 1 {
			return fmt.Errorf("execution.rounds[%d] must contain exactly one matching mode configuration", i)
		}
		switch r.Mode {
		case "translate":
			hasContentRound = true
			if r.Translate == nil {
				return fmt.Errorf("execution.rounds[%d] is missing translate settings", i)
			}
			t := r.Translate
			batch, words, concurrency, retry = t.BatchSize, t.MaxWordsPerBatch, t.Concurrency, t.Retry
			if t.FallbackShrink <= 0 || t.FallbackShrink > 1 {
				return fmt.Errorf("execution.rounds[%d] fallback_shrink must be in (0,1]", i)
			}
		case "extract":
			if r.Extract == nil {
				return fmt.Errorf("execution.rounds[%d] is missing extract settings", i)
			}
			t := r.Extract
			batch, words, concurrency, retry = t.BatchSize, t.MaxWordsPerBatch, t.Concurrency, t.Retry
		case "revise":
			hasContentRound = true
			if r.Revise == nil {
				return fmt.Errorf("execution.rounds[%d] is missing revise settings", i)
			}
			t := r.Revise
			batch, words, concurrency, retry = t.BatchSize, t.MaxWordsPerBatch, t.Concurrency, t.Retry
		default:
			return fmt.Errorf("execution.rounds[%d] uses an unsupported CLI mode", i)
		}
		if err := execution.ValidateBatchLimits(r.Mode, batch, words); err != nil {
			return fmt.Errorf("execution.rounds[%d].%s.%w", i, r.Mode, err)
		}
		if concurrency < 1 || retry.MaxAttempts < 0 || retry.BackoffMs < 0 {
			return fmt.Errorf("execution.rounds[%d] has invalid concurrency or retry settings", i)
		}
		if _, ok := cfg.Backends[r.Backend]; !ok {
			return fmt.Errorf("execution.rounds[%d] references an unknown backend", i)
		}
	}
	if !hasContentRound {
		return errors.New("execution.rounds must include a translate or revise round")
	}
	if cfg.TranslationMemory.Enabled || cfg.TranslationMemory.Driver != "" || cfg.TranslationMemory.DSN != "" {
		return errors.New("translation_memory settings are not supported in CLI mode")
	}
	if cfg.Plugins.Enabled || len(cfg.Plugins.Scripts) > 0 {
		return errors.New("plugins settings are not supported in CLI mode")
	}
	if cfg.Output.Mode != "" || cfg.Output.PreserveExtension || cfg.Output.Incremental {
		return errors.New("output settings are not supported in CLI mode; use --output")
	}
	for name, p := range cfg.TranslationProfiles {
		if err := execution.ValidateProfile(p.ProfileSpec); err != nil {
			return fmt.Errorf("translation_profiles[%s]: %w", name, err)
		}
		q := p.QA
		defaultQA := execution.DefaultProfile().QA
		if q.Enabled || q.AutoReject || len(q.Checks) > 0 ||
			(q.LengthMethod != "" && q.LengthMethod != defaultQA.LengthMethod) ||
			(q.LengthRatioMin != 0 && q.LengthRatioMin != defaultQA.LengthRatioMin) ||
			(q.LengthRatioMax != 0 && q.LengthRatioMax != defaultQA.LengthRatioMax) {
			return fmt.Errorf("translation_profiles[%s].qa is not supported in CLI mode", name)
		}
	}
	for name, b := range cfg.Backends {
		if _, ok := b.Options["api_key"]; ok {
			return fmt.Errorf("backends[%s].options.api_key is unsupported; use secret", name)
		}
		if b.Type != "openai" && b.Type != "anthropic" && b.Type != "google" {
			return fmt.Errorf("backends[%s].type is unsupported", name)
		}
		if b.RateLimitPerMinute < 0 {
			return fmt.Errorf("backends[%s].rate_limit_per_minute must not be negative", name)
		}
		if _, err := execution.ResolveBackendOptions(b.Type, b.Options); err != nil {
			return fmt.Errorf("backends[%s].options: %w", name, err)
		}
	}
	if cfg.Log.Level != "debug" && cfg.Log.Level != "info" && cfg.Log.Level != "warn" && cfg.Log.Level != "error" {
		return errors.New("log.level must be debug, info, warn or error")
	}
	if cfg.Log.Format != "text" && cfg.Log.Format != "json" {
		return errors.New("log.format must be text or json")
	}
	return nil
}

func parseTranslationYAML(data []byte) (*yaml.Node, error) {
	var doc, extra yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&doc); err != nil {
		return nil, errors.New("invalid YAML document")
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("exactly one YAML document is required")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("YAML document must be an object")
	}
	if err := validateTranslationTree(doc.Content[0], "document"); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}
func validateTranslationTree(node *yaml.Node, path string) error {
	if node.Kind == yaml.AliasNode || node.Tag == "!!null" {
		return fmt.Errorf("%s: aliases and null are not supported", path)
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Tag != "!!str" {
				return fmt.Errorf("%s: keys must be strings", path)
			}
			if seen[key.Value] {
				return fmt.Errorf("%s: duplicate key %s", path, key.Value)
			}
			seen[key.Value] = true
			if err := validateTranslationTree(node.Content[i+1], path+"."+key.Value); err != nil {
				return err
			}
		}
	} else if node.Kind == yaml.SequenceNode {
		for i, child := range node.Content {
			if err := validateTranslationTree(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func expandTranslationScalars(node *yaml.Node, path string, env map[string]string) error {
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i].Value
			next := path + "." + key
			if key == "content" && (strings.HasPrefix(path, ".translation_prompt_templates.") || strings.HasPrefix(path, ".bootstrap_prompt_templates.")) {
				continue
			}
			if err := expandTranslationScalars(node.Content[i+1], next, env); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if err := expandTranslationScalars(child, path, env); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Tag == "!!str" {
		value, err := ExpandReferences(node.Value, env)
		if err != nil {
			return fmt.Errorf("%s: %w", strings.TrimPrefix(path, "."), err)
		}
		node.Value = value
	}
	return nil
}
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func strictTranslationDecode(node *yaml.Node, dst any, path string) error {
	if node == nil {
		return fmt.Errorf("%s is required", path)
	}
	if err := validateTranslationType(node, reflect.TypeOf(dst).Elem(), path); err != nil {
		return err
	}
	if err := node.Decode(dst); err != nil {
		return fmt.Errorf("%s has an invalid value", path)
	}
	return nil
}
func yamlStructFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		if len(parts) > 1 && parts[1] == "inline" {
			for k, v := range yamlStructFields(f.Type) {
				fields[k] = v
			}
			continue
		}
		name := parts[0]
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	return fields
}
func validateTranslationType(node *yaml.Node, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s must be an object", path)
		}
		fields := yamlStructFields(t)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i].Value
			field, ok := fields[key]
			if !ok {
				return fmt.Errorf("%s: unknown field %s", path, key)
			}
			if err := validateTranslationType(node.Content[i+1], field, path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s must be an object", path)
		}
		for i := 0; i < len(node.Content); i += 2 {
			if err := validateTranslationType(node.Content[i+1], t.Elem(), path+"."+node.Content[i].Value); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if node.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s must be an array", path)
		}
		for i, child := range node.Content {
			if err := validateTranslationType(child, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.String:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return fmt.Errorf("%s must be a string", path)
		}
	case reflect.Bool:
		if node.Tag != "!!bool" || (node.Value != "true" && node.Value != "false") {
			return fmt.Errorf("%s must be true or false", path)
		}
	case reflect.Int, reflect.Int64:
		if node.Tag != "!!int" {
			return fmt.Errorf("%s must be an integer", path)
		}
	case reflect.Float32, reflect.Float64:
		if node.Tag != "!!float" && node.Tag != "!!int" {
			return fmt.Errorf("%s must be a number", path)
		}
	case reflect.Interface: // Provider 选项在执行解析阶段有各自的显式校验。
	default:
		return fmt.Errorf("%s has an unsupported field type", path)
	}
	return nil
}

// readExternalFileBytes 在检查路径包含关系之前先解析符号链接。
func readExternalFileBytes(ref, base string) ([]byte, error) {
	if filepath.IsAbs(ref) {
		return nil, errors.New("absolute references are not allowed")
	}
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, errors.New("cannot resolve configuration directory")
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, ref))
	if err != nil {
		return nil, errors.New("cannot read referenced file")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("reference escapes the configuration directory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read referenced file")
	}
	return data, nil
}
