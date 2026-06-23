package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// writeTempFile 在 t.TempDir() 下创建文件并写入内容，返回绝对路径。
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempFile: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Test 1: LoadCLIConfig("") 从嵌入模板生成默认配置
// ---------------------------------------------------------------------------

func TestLoadCLIConfig_Default(t *testing.T) {
	cfg, err := LoadCLIConfig("")
	if err != nil {
		t.Fatalf("LoadCLIConfig(\"\") error: %v", err)
	}

	// ── 验证 backends ──
	be, ok := cfg.Backends["openai-default"]
	if !ok {
		t.Fatal("expected backend \"openai-default\" in Backends map")
	}
	if be.Type != "openai" {
		t.Errorf("backend type = %q, want %q", be.Type, "openai")
	}

	// ── 验证 prompt_templates（file 引用应被 resolveEmbeddedReferences 解析为 content）──
	pt, ok := cfg.PromptTemplates["通用提示词"]
	if !ok {
		t.Fatal("expected prompt_template \"通用提示词\" in PromptTemplates map")
	}
	if pt.Content == "" {
		t.Error("prompt content should be resolved from embedded file, got empty")
	}
	if pt.File != "" {
		t.Errorf("prompt file should be cleared after resolution, got %q", pt.File)
	}

	// ── 验证 translation_profiles（file 引用应被解析）──
	prof, ok := cfg.TranslationProfiles["通用策略"]
	if !ok {
		t.Fatalf("expected profile \"通用策略\" in TranslationProfiles map; keys: %v", mapKeys(cfg.TranslationProfiles))
	}
	if prof.Split.Strategy != "paragraph" {
		t.Errorf("profile split.strategy = %q, want %q", prof.Split.Strategy, "paragraph")
	}
	if prof.Split.MaxChars != 1200 {
		t.Errorf("profile split.max_chars = %d, want 1200", prof.Split.MaxChars)
	}
	if !prof.Repair.Enabled {
		t.Error("expected repair.enabled = true")
	}

	// ── 验证 execution rounds ──
	if len(cfg.Execution.Rounds) != 1 {
		t.Fatalf("execution rounds = %d, want 1", len(cfg.Execution.Rounds))
	}
	r := cfg.Execution.Rounds[0]
	if r.Backend != "openai-default" {
		t.Errorf("round backend = %q, want %q", r.Backend, "openai-default")
	}
	if r.Prompt != "通用提示词" {
		t.Errorf("round prompt = %q, want %q", r.Prompt, "通用提示词")
	}
	if r.Profile != "通用策略" {
		t.Errorf("round profile = %q, want %q", r.Profile, "通用策略")
	}
	if r.Retry.Jitter != true {
		t.Error("expected retry.jitter = true")
	}
}

// TestDefaultCLIConfig_Fallback 验证 defaultCLIConfig() 回退路径的基本完整性。
func TestDefaultCLIConfig_Fallback(t *testing.T) {
	cfg := defaultCLIConfig()
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
	if _, ok := cfg.Backends["openai-default"]; !ok {
		t.Error("expected fallback backend \"openai-default\"")
	}
}

// ---------------------------------------------------------------------------
// Test 2: LoadCLIConfig 新格式 YAML
// ---------------------------------------------------------------------------

func TestLoadCLIConfig_NewFormat(t *testing.T) {
	yaml := `
version: 1
source_lang: en
target_lang: ja
backends:
  gpt4:
    type: openai
    enabled: true
    options:
      model: gpt-4o
prompt_templates:
  tech:
    content: "You are a technical translator."
translation_profiles:
  subtitle:
    split:
      enabled: true
      strategy: newline
      max_chars: 80
    repair:
      enabled: true
execution:
  rounds:
    - name: "首轮"
      backend: gpt4
      prompt: tech
      profile: subtitle
      batch_size: 5
      concurrency: 2
`
	path := writeTempFile(t, "new-format.yaml", yaml)

	cfg, err := LoadCLIConfig(path)
	if err != nil {
		t.Fatalf("LoadCLIConfig(new-format) error: %v", err)
	}

	if cfg.SourceLang != "en" {
		t.Errorf("source_lang = %q, want %q", cfg.SourceLang, "en")
	}
	if cfg.TargetLang != "ja" {
		t.Errorf("target_lang = %q, want %q", cfg.TargetLang, "ja")
	}

	be, ok := cfg.Backends["gpt4"]
	if !ok {
		t.Fatal("expected backend \"gpt4\"")
	}
	if be.Type != "openai" {
		t.Errorf("backend type = %q, want %q", be.Type, "openai")
	}

	pt, ok := cfg.PromptTemplates["tech"]
	if !ok {
		t.Fatal("expected prompt_template \"tech\"")
	}
	if pt.Content != "You are a technical translator." {
		t.Errorf("prompt content = %q", pt.Content)
	}

	prof, ok := cfg.TranslationProfiles["subtitle"]
	if !ok {
		t.Fatal("expected profile \"subtitle\"")
	}
	if prof.Split.Strategy != "newline" {
		t.Errorf("split.strategy = %q, want %q", prof.Split.Strategy, "newline")
	}

	if len(cfg.Execution.Rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(cfg.Execution.Rounds))
	}
	r := cfg.Execution.Rounds[0]
	if r.Backend != "gpt4" || r.Prompt != "tech" || r.Profile != "subtitle" {
		t.Errorf("round = {backend:%q, prompt:%q, profile:%q}, want {gpt4, tech, subtitle}",
			r.Backend, r.Prompt, r.Profile)
	}
	if r.BatchSize != 5 {
		t.Errorf("batch_size = %d, want 5", r.BatchSize)
	}
}

// ---------------------------------------------------------------------------
// Test 3: LoadCLIConfig 旧格式迁移
// ---------------------------------------------------------------------------

func TestLoadCLIConfig_LegacyMigration(t *testing.T) {
	// 旧格式：有 pipeline 字段，无 execution 字段
	yaml := `
version: 1
source_lang: auto
target_lang: zh
backends:
  - name: openai-primary
    type: openai
    enabled: true
    rate_limit_per_sec: 10
    options:
      model: gpt-4o-mini
  - name: anthropic-backup
    type: anthropic
    enabled: false
pipeline:
  split:
    enabled: true
    strategy: paragraph
    max_chars: 1500
  protect:
    enabled: true
    rules: [code, link]
  translate:
    concurrency: 8
    batch_size: 2
    fallback_shrink: 0.3
    rate_limit_per_sec: 10
    retry:
      max_attempts: 5
      backoff_ms: 3000
      jitter: true
    repair:
      enabled: true
      json_structural: true
      partial: true
      partial_threshold: 0.6
  postprocess:
    enabled: true
    trim_spaces: true
prompt:
  system_template_content: "你是专业翻译。"
glossary:
  enabled: false
  path: ./glossary.csv
`
	path := writeTempFile(t, "legacy.yaml", yaml)

	cfg, err := LoadCLIConfig(path)
	if err != nil {
		t.Fatalf("LoadCLIConfig(legacy) error: %v", err)
	}

	// ── 验证 backends 从数组迁移为 map ──
	if len(cfg.Backends) != 2 {
		t.Fatalf("backends count = %d, want 2", len(cfg.Backends))
	}
	be1, ok := cfg.Backends["openai-primary"]
	if !ok {
		t.Fatal("expected backend \"openai-primary\"")
	}
	if be1.RateLimitPerSec != 10 {
		t.Errorf("backend rate_limit_per_sec = %d, want 10", be1.RateLimitPerSec)
	}
	be2, ok := cfg.Backends["anthropic-backup"]
	if !ok {
		t.Fatal("expected backend \"anthropic-backup\"")
	}
	if be2.Enabled {
		t.Error("expected anthropic-backup to be disabled")
	}

	// ── 验证 prompt_templates ──
	pt, ok := cfg.PromptTemplates["default"]
	if !ok {
		t.Fatal("expected prompt_template \"default\"")
	}
	if pt.Content != "你是专业翻译。" {
		t.Errorf("prompt content = %q, want %q", pt.Content, "你是专业翻译。")
	}

	// ── 验证 translation_profiles ──
	prof, ok := cfg.TranslationProfiles["default"]
	if !ok {
		t.Fatal("expected profile \"default\"")
	}
	if prof.Split.MaxChars != 1500 {
		t.Errorf("split.max_chars = %d, want 1500", prof.Split.MaxChars)
	}
	if prof.Repair.PartialThreshold != 0.6 {
		t.Errorf("repair.partial_threshold = %v, want 0.6", prof.Repair.PartialThreshold)
	}

	// ── 验证 execution rounds（自动构造的单轮） ──
	if len(cfg.Execution.Rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(cfg.Execution.Rounds))
	}
	r := cfg.Execution.Rounds[0]
	if r.Backend != "openai-primary" {
		t.Errorf("round backend = %q, want %q", r.Backend, "openai-primary")
	}
	if r.Prompt != "default" {
		t.Errorf("round prompt = %q, want %q", r.Prompt, "default")
	}
	if r.Profile != "default" {
		t.Errorf("round profile = %q, want %q", r.Profile, "default")
	}
	if r.BatchSize != 2 {
		t.Errorf("batch_size = %d, want 2", r.BatchSize)
	}
	if r.Concurrency != 8 {
		t.Errorf("concurrency = %d, want 8", r.Concurrency)
	}
	if r.FallbackShrink != 0.3 {
		t.Errorf("fallback_shrink = %v, want 0.3", r.FallbackShrink)
	}
	if r.Retry.MaxAttempts != 5 {
		t.Errorf("retry.max_attempts = %d, want 5", r.Retry.MaxAttempts)
	}
	if r.Retry.BackoffMs != 3000 {
		t.Errorf("retry.backoff_ms = %d, want 3000", r.Retry.BackoffMs)
	}

	// ── 验证 Glossary 全局配置保留 ──
	if cfg.Glossary.Path != "./glossary.csv" {
		t.Errorf("glossary.path = %q, want %q", cfg.Glossary.Path, "./glossary.csv")
	}
}

// ---------------------------------------------------------------------------
// Test 4: resolveExternalReferences 路径安全
// ---------------------------------------------------------------------------

func TestResolveExternalReferences_PathTraversal(t *testing.T) {
	configDir := t.TempDir()

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"evil": {
				File: "../../../etc/passwd",
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err := resolveExternalReferences(cfg, configDir)
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
	if !strings.Contains(err.Error(), "禁止") && !strings.Contains(err.Error(), "遍历") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestResolveExternalReferences_AbsolutePath(t *testing.T) {
	configDir := t.TempDir()

	// 构造一个跨平台的绝对路径（filepath.IsAbs 在当前 OS 上必然返回 true）
	absPath, err := filepath.Abs(filepath.Join(configDir, "..", "outside.yaml"))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"evil": {
				File: absPath,
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err = resolveExternalReferences(cfg, configDir)
	if err == nil {
		t.Fatal("expected error for absolute path, got nil")
	}
	// 绝对路径应返回 "绝对路径" 错误
	if !strings.Contains(err.Error(), "绝对路径") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestResolveExternalReferences_ProfilePathTraversal 验证 translation_profiles 的路径安全。
func TestResolveExternalReferences_ProfilePathTraversal(t *testing.T) {
	configDir := t.TempDir()

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{
			"evil": {
				File: "../../secret.yaml",
			},
		},
	}

	err := resolveExternalReferences(cfg, configDir)
	if err == nil {
		t.Fatal("expected error for profile path traversal, got nil")
	}
}

// TestResolveExternalReferences_ValidFile 验证合法外部文件引用正常读取。
func TestResolveExternalReferences_ValidFile(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("外部提示词内容"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"external": {
				File: "prompt.txt",
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err := resolveExternalReferences(cfg, dir)
	if err != nil {
		t.Fatalf("resolveExternalReferences error: %v", err)
	}

	pt := cfg.PromptTemplates["external"]
	if pt.Content != "外部提示词内容" {
		t.Errorf("content = %q, want %q", pt.Content, "外部提示词内容")
	}
	if pt.File != "" {
		t.Errorf("file should be cleared after resolution, got %q", pt.File)
	}
}

// ---------------------------------------------------------------------------
// Test: Bootstrap 模板字段解析
// ---------------------------------------------------------------------------

// TestLoadCLIConfig_BootstrapContentInline 验证 bootstrap_content 内联字段正确解析。
func TestLoadCLIConfig_BootstrapContentInline(t *testing.T) {
	yamlContent := `
version: 1
source_lang: en
target_lang: zh
backends:
  gpt4:
    type: openai
    enabled: true
    options:
      model: gpt-4o
prompt_templates:
  tech:
    content: "You are a technical translator."
    bootstrap_content: "Extract domain terms from the text."
translation_profiles:
  default:
    split:
      enabled: true
      strategy: paragraph
      max_chars: 1200
execution:
  rounds:
    - name: "首轮"
      backend: gpt4
      prompt: tech
      profile: default
`
	path := writeTempFile(t, "bootstrap-inline.yaml", yamlContent)

	cfg, err := LoadCLIConfig(path)
	if err != nil {
		t.Fatalf("LoadCLIConfig error: %v", err)
	}

	pt, ok := cfg.PromptTemplates["tech"]
	if !ok {
		t.Fatal("expected prompt_template \"tech\"")
	}
	if pt.Content != "You are a technical translator." {
		t.Errorf("content = %q", pt.Content)
	}
	if pt.BootstrapContent != "Extract domain terms from the text." {
		t.Errorf("bootstrap_content = %q, want %q", pt.BootstrapContent, "Extract domain terms from the text.")
	}
}

// TestResolveExternalReferences_BootstrapFile 验证 bootstrap_file 外部引用正确解析。
func TestResolveExternalReferences_BootstrapFile(t *testing.T) {
	dir := t.TempDir()
	bootstrapFile := filepath.Join(dir, "bootstrap.txt")
	if err := os.WriteFile(bootstrapFile, []byte("Bootstrap 提示词内容"), 0o644); err != nil {
		t.Fatalf("write bootstrap file: %v", err)
	}

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"tech": {
				Content:       "翻译提示词",
				BootstrapFile: "bootstrap.txt",
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err := resolveExternalReferences(cfg, dir)
	if err != nil {
		t.Fatalf("resolveExternalReferences error: %v", err)
	}

	pt := cfg.PromptTemplates["tech"]
	if pt.BootstrapContent != "Bootstrap 提示词内容" {
		t.Errorf("bootstrap_content = %q, want %q", pt.BootstrapContent, "Bootstrap 提示词内容")
	}
	if pt.BootstrapFile != "" {
		t.Errorf("bootstrap_file should be cleared after resolution, got %q", pt.BootstrapFile)
	}
	// Content 不受影响
	if pt.Content != "翻译提示词" {
		t.Errorf("content = %q, want %q", pt.Content, "翻译提示词")
	}
}

// TestResolveExternalReferences_BootstrapPathTraversal 验证 bootstrap_file 的路径安全。
func TestResolveExternalReferences_BootstrapPathTraversal(t *testing.T) {
	configDir := t.TempDir()

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"evil": {
				BootstrapFile: "../../../etc/passwd",
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err := resolveExternalReferences(cfg, configDir)
	if err == nil {
		t.Fatal("expected error for bootstrap_file path traversal, got nil")
	}
	if !strings.Contains(err.Error(), "禁止") && !strings.Contains(err.Error(), "遍历") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestResolveExternalReferences_BootstrapAbsolutePath 验证 bootstrap_file 禁止绝对路径。
func TestResolveExternalReferences_BootstrapAbsolutePath(t *testing.T) {
	configDir := t.TempDir()

	absPath, err := filepath.Abs(filepath.Join(configDir, "..", "outside.yaml"))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"evil": {
				BootstrapFile: absPath,
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err = resolveExternalReferences(cfg, configDir)
	if err == nil {
		t.Fatal("expected error for bootstrap_file absolute path, got nil")
	}
	if !strings.Contains(err.Error(), "绝对路径") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestLoadCLIConfig_Default_BootstrapContent 验证默认配置从嵌入模板解析 bootstrap 模板。
func TestLoadCLIConfig_Default_BootstrapContent(t *testing.T) {
	cfg, err := LoadCLIConfig("")
	if err != nil {
		t.Fatalf("LoadCLIConfig(\"\") error: %v", err)
	}

	pt, ok := cfg.PromptTemplates["通用提示词"]
	if !ok {
		t.Fatal("expected prompt_template \"通用提示词\" in PromptTemplates map")
	}
	// 内置模板应从嵌入 FS 解析 bootstrap_file 为 bootstrap_content
	if pt.BootstrapContent == "" {
		t.Error("bootstrap_content should be resolved from embedded file, got empty")
	}
	if pt.BootstrapFile != "" {
		t.Errorf("bootstrap_file should be cleared after resolution, got %q", pt.BootstrapFile)
	}
}

// TestLoadCLIConfig_BootstrapContentPriority 验证 bootstrap_content 优先于 bootstrap_file。
func TestLoadCLIConfig_BootstrapContentPriority(t *testing.T) {
	dir := t.TempDir()
	bootstrapFile := filepath.Join(dir, "bootstrap.txt")
	if err := os.WriteFile(bootstrapFile, []byte("来自文件的 bootstrap"), 0o644); err != nil {
		t.Fatalf("write bootstrap file: %v", err)
	}

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"tech": {
				Content:          "翻译提示词",
				BootstrapContent: "来自内联的 bootstrap",
				BootstrapFile:    "bootstrap.txt",
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err := resolveExternalReferences(cfg, dir)
	if err != nil {
		t.Fatalf("resolveExternalReferences error: %v", err)
	}

	pt := cfg.PromptTemplates["tech"]
	// bootstrap_content 已有值时，不应被 bootstrap_file 覆盖
	if pt.BootstrapContent != "来自内联的 bootstrap" {
		t.Errorf("bootstrap_content = %q, want %q (inline should take priority)", pt.BootstrapContent, "来自内联的 bootstrap")
	}
}

// ---------------------------------------------------------------------------
// Test 5: migrateFromLegacy 完整性
// ---------------------------------------------------------------------------

func TestMigrateFromLegacy(t *testing.T) {
	legacy := &Config{
		Version:    1,
		SourceLang: "en",
		TargetLang: "de",
		Backends: []BackendConfig{
			{
				Name:            "openai-main",
				Type:            "openai",
				Enabled:         true,
				RateLimitPerSec: 5,
				Options:         map[string]any{"model": "gpt-4o", "temperature": 0.3},
			},
			{
				Name:    "anthropic-fallback",
				Type:    "anthropic",
				Enabled: false,
			},
		},
		Pipeline: PipelineConfig{
			Split:   SplitConfig{Enabled: true, Strategy: "sentence", MaxChars: 800},
			Protect: ProtectConfig{Enabled: true, Rules: []string{"code", "xml"}},
			Translate: TranslateConfig{
				Concurrency:     6,
				BatchSize:       3,
				FallbackShrink:  0.4,
				RateLimitPerSec: 15,
				Retry:           RetryConfig{MaxAttempts: 4, BackoffMs: 2500, Jitter: true},
				Repair: RepairConfig{
					Enabled:              true,
					JSONStructural:       true,
					SchemaAliases:        true,
					Partial:              true,
					PartialThreshold:     0.7,
					PlaceholderNormalize: true,
					PromptUpgrade:        false,
				},
			},
			Postprocess: PostprocessConfig{Enabled: true, TrimSpaces: true},
		},
		Prompt: PromptConfig{
			SystemTemplateContent: "专业翻译引擎。",
		},
		Glossary: GlossaryConfig{
			Enabled: true,
			Path:    "./terms.csv",
		},
		Output: OutputConfig{Mode: "append", PreserveExtension: false},
		Log:    LogConfig{Level: "debug", Format: "json"},
	}

	cliCfg := migrateFromLegacy(legacy)

	// ── 基本字段 ──
	if cliCfg.Version != 1 {
		t.Errorf("version = %d, want 1", cliCfg.Version)
	}
	if cliCfg.SourceLang != "en" {
		t.Errorf("source_lang = %q, want %q", cliCfg.SourceLang, "en")
	}
	if cliCfg.TargetLang != "de" {
		t.Errorf("target_lang = %q, want %q", cliCfg.TargetLang, "de")
	}

	// ── Backends 数组 → map ──
	if len(cliCfg.Backends) != 2 {
		t.Fatalf("backends count = %d, want 2", len(cliCfg.Backends))
	}
	be := cliCfg.Backends["openai-main"]
	if be.Type != "openai" {
		t.Errorf("backend type = %q, want %q", be.Type, "openai")
	}
	if be.RateLimitPerSec != 5 {
		t.Errorf("backend rate_limit_per_sec = %d, want 5", be.RateLimitPerSec)
	}
	if be.Options["model"] != "gpt-4o" {
		t.Errorf("backend options[model] = %v, want %q", be.Options["model"], "gpt-4o")
	}
	be2 := cliCfg.Backends["anthropic-fallback"]
	if be2.Enabled {
		t.Error("expected anthropic-fallback disabled")
	}

	// ── PromptTemplates ──
	pt, ok := cliCfg.PromptTemplates["default"]
	if !ok {
		t.Fatal("expected prompt \"default\"")
	}
	if pt.Content != "专业翻译引擎。" {
		t.Errorf("prompt content = %q, want %q", pt.Content, "专业翻译引擎。")
	}

	// ── TranslationProfiles ──
	prof, ok := cliCfg.TranslationProfiles["default"]
	if !ok {
		t.Fatal("expected profile \"default\"")
	}
	if prof.Split.Strategy != "sentence" {
		t.Errorf("split.strategy = %q, want %q", prof.Split.Strategy, "sentence")
	}
	if prof.Split.MaxChars != 800 {
		t.Errorf("split.max_chars = %d, want 800", prof.Split.MaxChars)
	}
	if !prof.Repair.PlaceholderNormalize {
		t.Error("expected repair.placeholder_normalize = true")
	}
	if prof.Repair.PromptUpgrade {
		t.Error("expected repair.prompt_upgrade = false")
	}

	// ── Execution ──
	if len(cliCfg.Execution.Rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(cliCfg.Execution.Rounds))
	}
	r := cliCfg.Execution.Rounds[0]
	if r.Name != "主翻译" {
		t.Errorf("round name = %q, want %q", r.Name, "主翻译")
	}
	if r.Backend != "openai-main" {
		t.Errorf("round backend = %q, want %q", r.Backend, "openai-main")
	}
	if r.BatchSize != 3 {
		t.Errorf("batch_size = %d, want 3", r.BatchSize)
	}
	if r.Concurrency != 6 {
		t.Errorf("concurrency = %d, want 6", r.Concurrency)
	}
	if r.FallbackShrink != 0.4 {
		t.Errorf("fallback_shrink = %v, want 0.4", r.FallbackShrink)
	}
	if r.RateLimitPerSec != 15 {
		t.Errorf("rate_limit_per_sec = %d, want 15", r.RateLimitPerSec)
	}
	if r.Retry.MaxAttempts != 4 {
		t.Errorf("retry.max_attempts = %d, want 4", r.Retry.MaxAttempts)
	}
	if r.Retry.BackoffMs != 2500 {
		t.Errorf("retry.backoff_ms = %d, want 2500", r.Retry.BackoffMs)
	}
	if !r.Retry.Jitter {
		t.Error("expected retry.jitter = true")
	}

	// ── 全局共享配置 ──
	if !cliCfg.Glossary.Enabled {
		t.Error("expected glossary.enabled = true")
	}
	if cliCfg.Glossary.Path != "./terms.csv" {
		t.Errorf("glossary.path = %q, want %q", cliCfg.Glossary.Path, "./terms.csv")
	}
	if cliCfg.Output.Mode != "append" {
		t.Errorf("output.mode = %q, want %q", cliCfg.Output.Mode, "append")
	}
	if cliCfg.Log.Level != "debug" {
		t.Errorf("log.level = %q, want %q", cliCfg.Log.Level, "debug")
	}
}

// TestMigrateFromLegacy_EmptyBackends 验证空 backends 时不 panic。
func TestMigrateFromLegacy_EmptyBackends(t *testing.T) {
	legacy := &Config{
		Version: 1,
		Pipeline: PipelineConfig{
			Translate: TranslateConfig{
				Concurrency: 4,
				BatchSize:   1,
			},
		},
	}

	cliCfg := migrateFromLegacy(legacy)

	if len(cliCfg.Execution.Rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(cliCfg.Execution.Rounds))
	}
	// 空 backends 时 round.Backend 应为空字符串
	if cliCfg.Execution.Rounds[0].Backend != "" {
		t.Errorf("expected empty backend, got %q", cliCfg.Execution.Rounds[0].Backend)
	}
}

// ---------------------------------------------------------------------------
// Test 6: Version 校验
// ---------------------------------------------------------------------------

func TestLoadCLIConfig_UnsupportedVersion(t *testing.T) {
	yaml := `
version: 99
source_lang: auto
target_lang: zh
`
	path := writeTempFile(t, "bad-version.yaml", yaml)

	_, err := LoadCLIConfig(path)
	if err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported config version") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 额外覆盖
// ---------------------------------------------------------------------------

// TestLoadCLIConfig_FileNotFound 验证文件不存在时返回 ErrConfigNotFound。
func TestLoadCLIConfig_FileNotFound(t *testing.T) {
	_, err := LoadCLIConfig("/nonexistent/path/linguaflow.yaml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestLoadCLIConfig_EnvExpansion 验证 ${ENV} 占位符展开。
func TestLoadCLIConfig_EnvExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "sk-test-12345")

	yaml := `
version: 1
backends:
  test:
    type: openai
    enabled: true
    options:
      api_key: ${TEST_API_KEY}
execution:
  rounds:
    - name: "test"
      backend: test
      prompt: default
      profile: default
`
	path := writeTempFile(t, "env-expand.yaml", yaml)

	cfg, err := LoadCLIConfig(path)
	if err != nil {
		t.Fatalf("LoadCLIConfig error: %v", err)
	}

	be := cfg.Backends["test"]
	if be.Options["api_key"] != "sk-test-12345" {
		t.Errorf("api_key = %v, want %q", be.Options["api_key"], "sk-test-12345")
	}
}

// TestProbeFormat 验证新旧格式探测。
func TestProbeFormat(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		wantExecution bool
		wantPipeline  bool
	}{
		{
			name:          "new format",
			yaml:          "execution:\n  rounds: []\n",
			wantExecution: true,
			wantPipeline:  false,
		},
		{
			name:          "legacy format",
			yaml:          "pipeline:\n  split:\n    enabled: true\n",
			wantExecution: false,
			wantPipeline:  true,
		},
		{
			name:          "empty",
			yaml:          "version: 1\n",
			wantExecution: false,
			wantPipeline:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotExec, gotPipe := probeFormat([]byte(tt.yaml))
			if gotExec != tt.wantExecution {
				t.Errorf("hasExecution = %v, want %v", gotExec, tt.wantExecution)
			}
			if gotPipe != tt.wantPipeline {
				t.Errorf("hasPipeline = %v, want %v", gotPipe, tt.wantPipeline)
			}
		})
	}
}

// TestReadExternalFileBytes_Security 验证 readExternalFileBytes 的安全检查。
func TestReadExternalFileBytes_Security(t *testing.T) {
	configDir := t.TempDir()

	t.Run("absolute path rejected", func(t *testing.T) {
		_, err := readExternalFileBytes("/etc/passwd", configDir)
		if err == nil {
			t.Fatal("expected error for absolute path")
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		_, err := readExternalFileBytes("../../../etc/passwd", configDir)
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
	})

	t.Run("valid relative path", func(t *testing.T) {
		// 在 configDir 下创建文件
		subDir := filepath.Join(configDir, "prompts")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(subDir, "test.txt"), []byte("hello"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		data, err := readExternalFileBytes("prompts/test.txt", configDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "hello" {
			t.Errorf("data = %q, want %q", string(data), "hello")
		}
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mapKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
