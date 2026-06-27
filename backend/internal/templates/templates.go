// Package templates 加载并提供内置模板。
// 从嵌入的 default/ 目录读取 linguaflow.yaml、prompts/、profiles/。
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	yaml "gopkg.in/yaml.v3"
)

//go:embed default
var builtinFS embed.FS

// EmbeddedFS 返回嵌入的 default/ 文件系统。
// 用于 DefaultCLIConfigFromBuiltins 从嵌入 FS 解析 file 引用。
func EmbeddedFS() embed.FS {
	return builtinFS
}

// DefaultConfigYAML 返回带注释的默认 CLIConfig YAML 模板字节。
// 从嵌入 FS 读取，避免与 builtinFS 的双重嵌入。
func DefaultConfigYAML() []byte {
	data, err := fs.ReadFile(builtinFS, "default/linguaflow.yaml")
	if err != nil {
		panic(fmt.Sprintf("embedded linguaflow.yaml not found: %v", err))
	}
	return data
}

// EmbeddedPromptTemplate 返回嵌入的默认提示词模板内容。
func EmbeddedPromptTemplate() string {
	data, err := fs.ReadFile(builtinFS, "default/prompts/default.tmpl")
	if err != nil {
		panic(fmt.Sprintf("embedded prompts/default.tmpl not found: %v", err))
	}
	return strings.TrimRight(string(data), "\n")
}

// EmbeddedBootstrapTemplate 返回嵌入的 bootstrap 术语抽取提示词模板内容。
func EmbeddedBootstrapTemplate() string {
	data, err := fs.ReadFile(builtinFS, "default/prompts/bootstrap_system.tmpl")
	if err != nil {
		panic(fmt.Sprintf("embedded prompts/bootstrap_system.tmpl not found: %v", err))
	}
	return strings.TrimRight(string(data), "\n")
}

// EmbeddedProfileConfig 返回嵌入的默认翻译策略配置字节。
func EmbeddedProfileConfig() []byte {
	data, err := fs.ReadFile(builtinFS, "default/profiles/default.yaml")
	if err != nil {
		panic(fmt.Sprintf("embedded profiles/default.yaml not found: %v", err))
	}
	return data
}

// ── 内置模板常量 ──────────────────────────────────────────────

const (
	// BuiltinPromptTemplateID 内置提示词模板的虚拟 ID。
	BuiltinPromptTemplateID = -1
	// BuiltinTranslationProfileID 内置翻译策略的虚拟 ID。
	BuiltinTranslationProfileID = -1
)

// IsBuiltinID 报告 id 是否为内置虚拟实体的负数 ID。
func IsBuiltinID(id int) bool { return id < 0 }

// ── config.yaml 元数据解析 ───────────────────────────────────

// builtinMeta 从 config.yaml 中解析的单条内置实体元数据。
type builtinMeta struct {
	ID          int    `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// builtinConfig 对应 config.yaml 的顶层结构（仅解析所需字段）。
type builtinConfig struct {
	PromptTemplate     builtinMeta `yaml:"prompt_template"`
	TranslationProfile builtinMeta `yaml:"translation_profile"`
}

// parseBuiltinConfig 从嵌入 FS 解析 config.yaml 元数据。
func parseBuiltinConfig() builtinConfig {
	data, err := fs.ReadFile(builtinFS, "default/config.yaml")
	if err != nil {
		panic(fmt.Sprintf("embedded config.yaml not found: %v", err))
	}
	var cfg builtinConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		panic(fmt.Sprintf("failed to parse embedded config.yaml: %v", err))
	}
	return cfg
}

// ── 内置 PromptTemplate 虚拟实体 ────────────────────────────

var builtinPromptTemplate *ent.PromptTemplate

func init() {
	meta := parseBuiltinConfig()
	builtinPromptTemplate = &ent.PromptTemplate{
		ID:                     BuiltinPromptTemplateID,
		Name:                   meta.PromptTemplate.Name,
		Description:            meta.PromptTemplate.Description,
		Scope:                  "system",
		SystemPromptContent:    EmbeddedPromptTemplate(),
		BootstrapPromptContent: EmbeddedBootstrapTemplate(),
	}
}

// BuiltinPromptTemplates 返回所有内置提示词模板（当前仅一个）。
func BuiltinPromptTemplates() []*ent.PromptTemplate {
	return []*ent.PromptTemplate{builtinPromptTemplate}
}

// BuiltinPromptTemplate 根据 id 返回内置提示词模板，id 不匹配时返回 nil。
func BuiltinPromptTemplate(id int) *ent.PromptTemplate {
	if id == BuiltinPromptTemplateID {
		return builtinPromptTemplate
	}
	return nil
}

// ── 内置 TranslationProfile 虚拟实体 ───────────────────────

var builtinProfileConfig schema.TranslationProfileConfigData
var builtinTranslationProfile *ent.TranslationProfile

func init() {
	if err := yaml.Unmarshal(EmbeddedProfileConfig(), &builtinProfileConfig); err != nil {
		panic(fmt.Sprintf("failed to parse embedded profile config: %v", err))
	}
}

func init() {
	meta := parseBuiltinConfig()
	builtinTranslationProfile = &ent.TranslationProfile{
		ID:          BuiltinTranslationProfileID,
		Name:        meta.TranslationProfile.Name,
		Description: meta.TranslationProfile.Description,
		Scope:       "system",
		Config:      builtinProfileConfig,
	}
}

// BuiltinTranslationProfiles 返回所有内置翻译策略（当前仅一个）。
func BuiltinTranslationProfiles() []*ent.TranslationProfile {
	return []*ent.TranslationProfile{builtinTranslationProfile}
}

// BuiltinTranslationProfile 根据 id 返回内置翻译策略，id 不匹配时返回 nil。
func BuiltinTranslationProfile(id int) *ent.TranslationProfile {
	if id == BuiltinTranslationProfileID {
		return builtinTranslationProfile
	}
	return nil
}
