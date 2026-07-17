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

	// ── 验证 Glossary 为 CLIConfigGlossary 类型 ──
	if cfg.Glossary.Path != "./glossary.csv" {
		t.Errorf("glossary.path = %q, want %q", cfg.Glossary.Path, "./glossary.csv")
	}
	if !cfg.Glossary.Save {
		t.Error("expected glossary.save = true in default config")
	}

	// ── 验证 Execution.Bootstrap 默认配置 ──
	if cfg.Execution.Bootstrap.Enabled {
		t.Error("expected execution.bootstrap.enabled = false in default config")
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

	// ── 验证 Glossary 为 CLIConfigGlossary 类型 ──
	if !cfg.Glossary.Save {
		t.Error("expected glossary.save = true in default config")
	}
	if cfg.Glossary.Path != "./glossary.csv" {
		t.Errorf("glossary.path = %q, want %q", cfg.Glossary.Path, "./glossary.csv")
	}

	// ── 验证 TranslationProfiles 默认 Bootstrap 配置 ──
	defProf, ok := cfg.TranslationProfiles["default"]
	if !ok {
		t.Fatal("expected profile \"default\" in fallback config")
	}
	if defProf.Bootstrap.MaxTermsPer1000Chars != 3.0 {
		t.Errorf("profile bootstrap.max_terms_per_1000_chars = %v, want 3.0", defProf.Bootstrap.MaxTermsPer1000Chars)
	}
	if defProf.Bootstrap.InlineConflictStrategy != "rewrite-local" {
		t.Errorf("profile bootstrap.inline_conflict_strategy = %q, want %q",
			defProf.Bootstrap.InlineConflictStrategy, "rewrite-local")
	}

	// ── 验证 Execution.Bootstrap 默认配置 ──
	if cfg.Execution.Bootstrap.Enabled {
		t.Error("expected execution.bootstrap.enabled = false in default config")
	}
	if cfg.Execution.Bootstrap.BatchSize != 20 {
		t.Errorf("execution.bootstrap.batch_size = %d, want 20", cfg.Execution.Bootstrap.BatchSize)
	}
	if cfg.Execution.Bootstrap.Concurrency != 2 {
		t.Errorf("execution.bootstrap.concurrency = %d, want 2", cfg.Execution.Bootstrap.Concurrency)
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
translation_prompt_templates:
  tech:
    content: "You are a technical translator."
translation_profiles:
  subtitle:
    repair:
      enabled: true
glossary:
  enabled: true
  path: ./terms.csv
  save: true
execution:
  bootstrap:
    enabled: true
    batch_size: 10
    concurrency: 4
    max_terms_per_1000_chars: 25.0
    min_source_len: 3
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

	_, ok = cfg.TranslationProfiles["subtitle"]
	if !ok {
		t.Fatal("expected profile \"subtitle\"")
	}
	// ── 验证 Glossary 为 CLIConfigGlossary 类型 ──
	if !cfg.Glossary.Enabled {
		t.Error("expected glossary.enabled = true")
	}
	if cfg.Glossary.Path != "./terms.csv" {
		t.Errorf("glossary.path = %q, want %q", cfg.Glossary.Path, "./terms.csv")
	}
	if !cfg.Glossary.Save {
		t.Error("expected glossary.save = true")
	}

	// ── 验证 Execution.Bootstrap ──
	if !cfg.Execution.Bootstrap.Enabled {
		t.Error("expected execution.bootstrap.enabled = true")
	}
	if cfg.Execution.Bootstrap.BatchSize != 10 {
		t.Errorf("execution.bootstrap.batch_size = %d, want 10", cfg.Execution.Bootstrap.BatchSize)
	}
	if cfg.Execution.Bootstrap.Concurrency != 4 {
		t.Errorf("execution.bootstrap.concurrency = %d, want 4", cfg.Execution.Bootstrap.Concurrency)
	}
	if cfg.Execution.Bootstrap.MaxTermsPer1000Chars != 25.0 {
		t.Errorf("execution.bootstrap.max_terms_per_1000_chars = %v, want 25.0", cfg.Execution.Bootstrap.MaxTermsPer1000Chars)
	}
	if cfg.Execution.Bootstrap.MinSourceLen != 3 {
		t.Errorf("execution.bootstrap.min_source_len = %d, want 3", cfg.Execution.Bootstrap.MinSourceLen)
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
// Test 3: resolveExternalReferences 路径安全
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

// TestLoadCLIConfig_BootstrapContentInline 验证 bootstrap 内联字段正确解析。
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
translation_prompt_templates:
  tech:
    content: "You are a technical translator."
bootstrap_prompt_templates:
  tech-terms:
    content: "Extract domain terms from the text."
translation_profiles:
  default:
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
	bt, ok := cfg.BootstrapPromptTemplates["tech-terms"]
	if !ok {
		t.Fatal("expected bootstrap_prompt_template \"tech-terms\"")
	}
	if bt.Content != "Extract domain terms from the text." {
		t.Errorf("bootstrap content = %q, want %q", bt.Content, "Extract domain terms from the text.")
	}
}

// TestResolveExternalReferences_BootstrapFile 验证 bootstrap 外部引用正确解析。
func TestResolveExternalReferences_BootstrapFile(t *testing.T) {
	dir := t.TempDir()
	bootstrapFile := filepath.Join(dir, "bootstrap.txt")
	if err := os.WriteFile(bootstrapFile, []byte("Bootstrap 提示词内容"), 0o644); err != nil {
		t.Fatalf("write bootstrap file: %v", err)
	}

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"tech": {
				Content: "翻译提示词",
			},
		},
		BootstrapPromptTemplates: map[string]CLIConfigBootstrapTemplate{
			"tech-bootstrap": {
				File: "bootstrap.txt",
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err := resolveExternalReferences(cfg, dir)
	if err != nil {
		t.Fatalf("resolveExternalReferences error: %v", err)
	}

	bt := cfg.BootstrapPromptTemplates["tech-bootstrap"]
	if bt.Content != "Bootstrap 提示词内容" {
		t.Errorf("bootstrap content = %q, want %q", bt.Content, "Bootstrap 提示词内容")
	}
	if bt.File != "" {
		t.Errorf("bootstrap file should be cleared after resolution, got %q", bt.File)
	}
	// 翻译模板不受影响
	pt := cfg.PromptTemplates["tech"]
	if pt.Content != "翻译提示词" {
		t.Errorf("content = %q, want %q", pt.Content, "翻译提示词")
	}
}

// TestResolveExternalReferences_BootstrapPathTraversal 验证 bootstrap_file 的路径安全。
func TestResolveExternalReferences_BootstrapPathTraversal(t *testing.T) {
	configDir := t.TempDir()

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{},
		BootstrapPromptTemplates: map[string]CLIConfigBootstrapTemplate{
			"evil": {
				File: "../../../etc/passwd",
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
		PromptTemplates: map[string]CLIConfigPromptTemplate{},
		BootstrapPromptTemplates: map[string]CLIConfigBootstrapTemplate{
			"evil": {
				File: absPath,
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

	bt, ok := cfg.BootstrapPromptTemplates["通用术语抽取"]
	if !ok {
		t.Fatal("expected bootstrap_prompt_template \"通用术语抽取\" in BootstrapPromptTemplates map")
	}
	// 内置模板应从嵌入 FS 解析 file 为 content
	if bt.Content == "" {
		t.Error("bootstrap content should be resolved from embedded file, got empty")
	}
	if bt.File != "" {
		t.Errorf("bootstrap file should be cleared after resolution, got %q", bt.File)
	}
}

// TestLoadCLIConfig_BootstrapContentPriority 验证 bootstrap content 优先于 file。
func TestLoadCLIConfig_BootstrapContentPriority(t *testing.T) {
	dir := t.TempDir()
	bootstrapFile := filepath.Join(dir, "bootstrap.txt")
	if err := os.WriteFile(bootstrapFile, []byte("来自文件的 bootstrap"), 0o644); err != nil {
		t.Fatalf("write bootstrap file: %v", err)
	}

	cfg := &CLIConfig{
		PromptTemplates: map[string]CLIConfigPromptTemplate{
			"tech": {
				Content: "翻译提示词",
			},
		},
		BootstrapPromptTemplates: map[string]CLIConfigBootstrapTemplate{
			"tech-bootstrap": {
				Content: "来自内联的 bootstrap",
				File:    "bootstrap.txt",
			},
		},
		TranslationProfiles: map[string]CLIConfigTranslationProfile{},
	}

	err := resolveExternalReferences(cfg, dir)
	if err != nil {
		t.Fatalf("resolveExternalReferences error: %v", err)
	}

	bt := cfg.BootstrapPromptTemplates["tech-bootstrap"]
	// content 已有值时，不应被 file 覆盖
	if bt.Content != "来自内联的 bootstrap" {
		t.Errorf("bootstrap content = %q, want %q (inline should take priority)", bt.Content, "来自内联的 bootstrap")
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
