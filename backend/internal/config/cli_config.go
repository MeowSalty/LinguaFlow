package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// CLI 新配置结构体
// ---------------------------------------------------------------------------

// CLIConfigGlossary CLI 端的术语表本体配置。
// 自举相关的配置分别放在 Profile（内联）和 Execution（独立）中。
type CLIConfigGlossary struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
	Save    bool   `yaml:"save"`
}

// CLIConfig 是 CLI 端的完整配置结构。
// 与旧 Config 结构的区别：
//   - Backends / PromptTemplates / TranslationProfiles 以 map 存储，execution.rounds 按名称引用
//   - 不包含 ServerConfig（CLI 不需要 Web 服务器配置）
//   - Glossary 为全局共享，不嵌入 profile
type CLIConfig struct {
	// Version 配置格式版本号，当前固定为 1。
	// 未来格式升级时用于自动迁移逻辑。
	Version    int    `yaml:"version"`
	SourceLang string `yaml:"source_lang"`
	TargetLang string `yaml:"target_lang"`

	Backends            map[string]CLIConfigBackend            `yaml:"backends"`
	PromptTemplates     map[string]CLIConfigPromptTemplate     `yaml:"prompt_templates"`
	TranslationProfiles map[string]CLIConfigTranslationProfile `yaml:"translation_profiles"`
	Execution           CLIConfigExecution                     `yaml:"execution"`

	// Glossary 全局术语表配置，所有轮次共享。
	Glossary          CLIConfigGlossary `yaml:"glossary"`
	TranslationMemory TMConfig          `yaml:"translation_memory"`
	Plugins           PluginsConfig     `yaml:"plugins"`
	Output            OutputConfig      `yaml:"output"`
	Log               LogConfig         `yaml:"log"`
}

// CLIConfigBackend 后端配置。
type CLIConfigBackend struct {
	Type               string         `yaml:"type"`
	Enabled            bool           `yaml:"enabled"`
	RateLimitPerMinute int            `yaml:"rate_limit_per_minute"` // 后端级限流（每分钟）；0 表示不限速
	Options            map[string]any `yaml:"options"`
}

// CLIConfigPromptTemplate 提示词模板配置。
type CLIConfigPromptTemplate struct {
	Content          string `yaml:"content"`           // 翻译提示词内联内容
	File             string `yaml:"file"`              // 翻译提示词外部文件引用（与 Content 二选一）
	BootstrapContent string `yaml:"bootstrap_content"` // bootstrap 模板内联内容
	BootstrapFile    string `yaml:"bootstrap_file"`    // bootstrap 模板外部文件引用（与 BootstrapContent 二选一）
}

// CLIConfigTranslationProfile 翻译策略配置。
// 注意：不包含 Glossary 字段。术语表使用 CLIConfig 全局的 Glossary 配置。
// 多轮共享同一份术语表，避免术语表实例化冲突。
type CLIConfigTranslationProfile struct {
	Protect     ProtectConfig     `yaml:"protect"`
	Postprocess PostprocessConfig `yaml:"postprocess"`
	Repair      RepairConfig      `yaml:"repair"`
	Bootstrap   BootstrapConfig   `yaml:"bootstrap"`
	Context     ContextConfig     `yaml:"context"`
	Ruby        RubyConfig        `yaml:"ruby"`

	File string `yaml:"file"` // 外部文件引用（与内联字段二选一）
}

// CLIConfigExecution 执行计划配置。
type CLIConfigExecution struct {
	Bootstrap StandaloneBootstrapConfig `yaml:"bootstrap"`
	Rounds    []CLIConfigRound          `yaml:"rounds"`
}

// CLIConfigRound 单轮执行配置。
type CLIConfigRound struct {
	Name             string      `yaml:"name"`
	Backend          string      `yaml:"backend"` // 引用 backends key
	Prompt           string      `yaml:"prompt"`  // 引用 prompt_templates key
	Profile          string      `yaml:"profile"` // 引用 translation_profiles key
	BatchSize        int         `yaml:"batch_size"`
	MaxWordsPerBatch int         `yaml:"max_words_per_batch"`
	Concurrency      int         `yaml:"concurrency"`
	FallbackShrink   float64     `yaml:"fallback_shrink"`
	Retry            RetryConfig `yaml:"retry"`
}

// ---------------------------------------------------------------------------
// LoadCLIConfig
// ---------------------------------------------------------------------------

// LoadCLIConfig 从 path 读取 YAML 配置。
//   - 若 path 为空，从内置模板生成默认 CLIConfig。
func LoadCLIConfig(path string) (*CLIConfig, error) {
	// ── 1. path 为空 → 从内置模板生成 ──
	if path == "" {
		return DefaultCLIConfigFromBuiltins(), nil
	}

	// ── 2. 读取 YAML 文件 ──
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	// ── 3. 展开 ${ENV} 占位符 ──
	expanded := expandEnv(raw)

	// ── 4. 解析为 CLIConfig ──
	cliCfg := &CLIConfig{}
	if err := yaml.Unmarshal(expanded, cliCfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// ── 5. 初始化 map ──
	if cliCfg.Backends == nil {
		cliCfg.Backends = make(map[string]CLIConfigBackend)
	}
	if cliCfg.PromptTemplates == nil {
		cliCfg.PromptTemplates = make(map[string]CLIConfigPromptTemplate)
	}
	if cliCfg.TranslationProfiles == nil {
		cliCfg.TranslationProfiles = make(map[string]CLIConfigTranslationProfile)
	}

	// ── 6. 解析外部文件引用 ──
	configDir := filepath.Dir(path)
	if err := resolveExternalReferences(cliCfg, configDir); err != nil {
		return nil, fmt.Errorf("config: resolve external references: %w", err)
	}

	// ── 7. Version 校验 ──
	switch cliCfg.Version {
	case 0, 1:
		// 正常
	default:
		return nil, fmt.Errorf("unsupported config version: %d", cliCfg.Version)
	}

	return cliCfg, nil
}

// ---------------------------------------------------------------------------
// resolveExternalReferences — 外部文件引用解析
// ---------------------------------------------------------------------------

// resolveExternalReferences 解析 prompt_templates 和 translation_profiles 中的 file 引用。
//   - content 优先级高于 file；两者都为空时保留原样（使用内置默认值）。
//   - file 路径必须是相对路径，禁止绝对路径。
//   - 使用 filepath.Clean + 前缀校验，防止 ../ 路径遍历。
//   - 解析后的路径必须在 configDir 或其子目录内。
func resolveExternalReferences(cliCfg *CLIConfig, configDir string) error {
	absConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		return fmt.Errorf("resolve config dir: %w", err)
	}

	// ── prompt_templates ──
	for name, pt := range cliCfg.PromptTemplates {
		// 解析翻译提示词 file 引用
		if pt.Content == "" && pt.File != "" {
			content, err := readExternalFile(pt.File, absConfigDir)
			if err != nil {
				return fmt.Errorf("prompt_templates[%q].file: %w", name, err)
			}
			pt.Content = content
			pt.File = ""
		}
		// 解析 bootstrap 模板 file 引用
		if pt.BootstrapContent == "" && pt.BootstrapFile != "" {
			content, err := readExternalFile(pt.BootstrapFile, absConfigDir)
			if err != nil {
				return fmt.Errorf("prompt_templates[%q].bootstrap_file: %w", name, err)
			}
			pt.BootstrapContent = content
			pt.BootstrapFile = ""
		}
		cliCfg.PromptTemplates[name] = pt
	}

	// ── translation_profiles ──
	for name, tp := range cliCfg.TranslationProfiles {
		if tp.File == "" {
			continue
		}
		// 如果已有内联配置（split/protect/postprocess/repair 任一非零值），忽略 file
		if hasInlineProfileConfig(tp) {
			tp.File = ""
			cliCfg.TranslationProfiles[name] = tp
			continue
		}
		// 读取外部文件并解析为 profile 配置
		raw, err := readExternalFileBytes(tp.File, absConfigDir)
		if err != nil {
			return fmt.Errorf("translation_profiles[%q].file: %w", name, err)
		}
		var extProfile CLIConfigTranslationProfile
		if err := yaml.Unmarshal(raw, &extProfile); err != nil {
			return fmt.Errorf("translation_profiles[%q].file parse: %w", name, err)
		}
		extProfile.File = "" // 已解析，清除 file 引用
		cliCfg.TranslationProfiles[name] = extProfile
	}

	return nil
}

// hasInlineProfileConfig 检查翻译策略是否有内联配置。
func hasInlineProfileConfig(tp CLIConfigTranslationProfile) bool {
	return len(tp.Protect.Rules) > 0 ||
		tp.Postprocess.TrimSpaces ||
		tp.Repair.Enabled
}

// readExternalFile 读取外部文件内容并返回字符串。
// relPath 必须是相对路径，解析后必须在 configDir 内。
func readExternalFile(relPath, configDir string) (string, error) {
	data, err := readExternalFileBytes(relPath, configDir)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// readExternalFileBytes 读取外部文件内容并返回字节切片。
// relPath 必须是相对路径，解析后必须在 configDir 内。
func readExternalFileBytes(relPath, configDir string) ([]byte, error) {
	// ── 安全检查：禁止绝对路径 ──
	if filepath.IsAbs(relPath) {
		return nil, fmt.Errorf("禁止绝对路径: %s", relPath)
	}

	// ── 解析并清理路径 ──
	joined := filepath.Join(configDir, relPath)
	absPath, err := filepath.Abs(joined)
	if err != nil {
		return nil, fmt.Errorf("解析路径：%w", err)
	}

	// ── 安全检查：防止路径遍历 ──
	cleanPath := filepath.Clean(absPath)
	if !strings.HasPrefix(cleanPath, filepath.Clean(configDir)+string(filepath.Separator)) &&
		cleanPath != filepath.Clean(configDir) {
		return nil, fmt.Errorf("路径遍历禁止: %s 解析为 %s，不在配置目录 %s 内",
			relPath, cleanPath, configDir)
	}

	// ── 读取文件 ──
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("读取文件 %s: %w", cleanPath, err)
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// DefaultCLIConfigFromBuiltins — 从内置模板生成默认 CLIConfig
// ---------------------------------------------------------------------------

// DefaultCLIConfigFromBuiltins 返回默认 CLIConfig。
// 从内置带注释 YAML 模板解析，与 init 输出共用同一数据源。
// file 引用指向嵌入 FS 中的模板文件，由 resolveEmbeddedReferences 解析。
func DefaultCLIConfigFromBuiltins() *CLIConfig {
	cliCfg := &CLIConfig{}
	if err := yaml.Unmarshal(templates.DefaultConfigYAML(), cliCfg); err != nil {
		// 模板损坏时回退到硬编码默认值
		return defaultCLIConfig()
	}

	// 初始化 map
	if cliCfg.Backends == nil {
		cliCfg.Backends = make(map[string]CLIConfigBackend)
	}
	if cliCfg.PromptTemplates == nil {
		cliCfg.PromptTemplates = make(map[string]CLIConfigPromptTemplate)
	}
	if cliCfg.TranslationProfiles == nil {
		cliCfg.TranslationProfiles = make(map[string]CLIConfigTranslationProfile)
	}

	// 从嵌入 FS 解析 file 引用
	if err := resolveEmbeddedReferences(cliCfg); err != nil {
		return defaultCLIConfig()
	}

	return cliCfg
}

// resolveEmbeddedReferences 从嵌入 FS 解析 prompt_templates 和
// translation_profiles 中的 file 引用。
// 与 resolveExternalReferences 功能相同，但数据源是嵌入 FS 而非用户文件系统。
func resolveEmbeddedReferences(cliCfg *CLIConfig) error {
	fsys := templates.EmbeddedFS()

	// ── prompt_templates ──
	for name, pt := range cliCfg.PromptTemplates {
		// 解析翻译提示词 file 引用
		if pt.Content == "" && pt.File != "" {
			data, err := fs.ReadFile(fsys, "default/"+pt.File)
			if err != nil {
				return fmt.Errorf("embedded prompt_templates[%q].file %q: %w", name, pt.File, err)
			}
			pt.Content = string(data)
			pt.File = ""
		}
		// 解析 bootstrap 模板 file 引用
		if pt.BootstrapContent == "" && pt.BootstrapFile != "" {
			data, err := fs.ReadFile(fsys, "default/"+pt.BootstrapFile)
			if err != nil {
				return fmt.Errorf("embedded prompt_templates[%q].bootstrap_file %q: %w", name, pt.BootstrapFile, err)
			}
			pt.BootstrapContent = string(data)
			pt.BootstrapFile = ""
		}
		cliCfg.PromptTemplates[name] = pt
	}

	// ── translation_profiles ──
	for name, tp := range cliCfg.TranslationProfiles {
		if tp.File == "" {
			continue
		}
		if hasInlineProfileConfig(tp) {
			tp.File = ""
			cliCfg.TranslationProfiles[name] = tp
			continue
		}
		data, err := fs.ReadFile(fsys, "default/"+tp.File)
		if err != nil {
			return fmt.Errorf("embedded translation_profiles[%q].file %q: %w", name, tp.File, err)
		}
		var extProfile CLIConfigTranslationProfile
		if err := yaml.Unmarshal(data, &extProfile); err != nil {
			return fmt.Errorf("embedded translation_profiles[%q].file parse: %w", name, err)
		}
		extProfile.File = ""
		cliCfg.TranslationProfiles[name] = extProfile
	}

	return nil
}

// defaultCLIConfig 返回一个硬编码的最小化默认 CLIConfig。
// 当内置模板不可用时使用。
func defaultCLIConfig() *CLIConfig {
	return &CLIConfig{
		Version:    1,
		SourceLang: "auto",
		TargetLang: "zh",
		Backends: map[string]CLIConfigBackend{
			"openai-default": {
				Type:    "openai",
				Enabled: true,
				Options: map[string]any{
					"api_key":         "${OPENAI_API_KEY}",
					"base_url":        "https://api.openai.com/v1",
					"model":           "gpt-4o-mini",
					"temperature":     0.2,
					"max_tokens":      0,
					"timeout":         "60s",
					"response_format": "json_schema",
				},
			},
		},
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"default": {}, // 空 content，使用内置默认值
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{
			"default": {
				Protect:     ProtectConfig{Enabled: true, Rules: []string{"code", "link", "placeholder", "xml"}},
				Postprocess: PostprocessConfig{Enabled: true, TrimSpaces: true},
				Repair: RepairConfig{
					Enabled:              true,
					JSONStructural:       true,
					SchemaAliases:        true,
					Partial:              true,
					PartialThreshold:     0.5,
					PlaceholderNormalize: true,
					PromptUpgrade:        true,
				},
				Bootstrap: BootstrapConfig{
					MaxTermsPer1000Chars:   3.0,
					MinSourceLen:           2,
					InlineConflictStrategy: InlineConflictRewriteLocal,
				},
			},
		},
		Execution: CLIConfigExecution{
			Bootstrap: StandaloneBootstrapConfig{
				Enabled:          false,
				BatchSize:        20,
				Concurrency:      2,
				MaxTermsPerBatch: 20,
				MinSourceLen:     2,
			},
			Rounds: []CLIConfigRound{{
				Name:           "主翻译",
				Backend:        "openai-default",
				Prompt:         "default",
				Profile:        "default",
				BatchSize:      1,
				Concurrency:    4,
				FallbackShrink: defaultFallbackShrink,
				Retry:          RetryConfig{MaxAttempts: 3, BackoffMs: 2000, Jitter: true},
			}},
		},
		Glossary: CLIConfigGlossary{
			Enabled: false,
			Path:    "./glossary.csv",
			Save:    true,
		},
		TranslationMemory: TMConfig{Enabled: false, Driver: "sqlite", DSN: "./.linguaflow/tm.db"},
		Plugins:           PluginsConfig{Enabled: false},
		Output:            OutputConfig{Mode: "overwrite", PreserveExtension: true},
		Log:               LogConfig{Level: "info", Format: "text"},
	}
}
