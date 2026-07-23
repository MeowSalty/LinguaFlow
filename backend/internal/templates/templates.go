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

// EmbeddedPromptTemplate 返回嵌入的默认翻译提示词模板内容。
func EmbeddedPromptTemplate() string {
	data, err := fs.ReadFile(builtinFS, "default/prompts/default_translation.tmpl")
	if err != nil {
		panic(fmt.Sprintf("embedded prompts/default_translation.tmpl not found: %v", err))
	}
	return strings.TrimRight(string(data), "\n")
}

// EmbeddedBootstrapTemplate 返回嵌入的 bootstrap 术语抽取提示词模板内容。
func EmbeddedBootstrapTemplate() string {
	data, err := fs.ReadFile(builtinFS, "default/prompts/default_bootstrap.tmpl")
	if err != nil {
		panic(fmt.Sprintf("embedded prompts/default_bootstrap.tmpl not found: %v", err))
	}
	return strings.TrimRight(string(data), "\n")
}

// EmbeddedPruneTemplate 返回嵌入的 prune 术语精简提示词模板内容。
func EmbeddedPruneTemplate() string {
	data, err := fs.ReadFile(builtinFS, "default/prompts/default_prune.tmpl")
	if err != nil {
		panic(fmt.Sprintf("embedded prompts/default_prune.tmpl not found: %v", err))
	}
	return strings.TrimRight(string(data), "\n")
}

// EmbeddedAdjudicationTemplate 返回嵌入的质量裁决提示词模板内容。
// 裁决 prompt 对用户不可见，仅供引擎内置使用。
func EmbeddedAdjudicationTemplate() string {
	data, err := fs.ReadFile(builtinFS, "default/prompts/default_adjudication.tmpl")
	if err != nil {
		panic(fmt.Sprintf("embedded prompts/default_adjudication.tmpl not found: %v", err))
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
	// BuiltinTranslationPromptTemplateID 内置翻译提示词模板的虚拟 ID。
	BuiltinTranslationPromptTemplateID = -1
	// BuiltinBootstrapPromptTemplateID 内置术语抽取提示词模板的虚拟 ID。
	BuiltinBootstrapPromptTemplateID = -1
	// BuiltinPrunePromptTemplateID 内置术语精简提示词模板的虚拟 ID。
	BuiltinPrunePromptTemplateID = -1
	// BuiltinExecutionProfileID 内置执行策略的虚拟 ID。
	BuiltinExecutionProfileID = -1
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
	TranslationPromptTemplate builtinMeta `yaml:"translation_prompt_template"`
	BootstrapPromptTemplate   builtinMeta `yaml:"bootstrap_prompt_template"`
	PrunePromptTemplate       builtinMeta `yaml:"prune_prompt_template"`
	TranslationProfile        builtinMeta `yaml:"translation_profile"`
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

// ── 内置 TranslationPromptTemplate 虚拟实体 ─────────────────

var builtinTranslationPromptTemplate *ent.TranslationPromptTemplate

func init() {
	meta := parseBuiltinConfig()
	builtinTranslationPromptTemplate = &ent.TranslationPromptTemplate{
		ID:                  BuiltinTranslationPromptTemplateID,
		Name:                meta.TranslationPromptTemplate.Name,
		Description:         meta.TranslationPromptTemplate.Description,
		Scope:               "system",
		SystemPromptContent: EmbeddedPromptTemplate(),
	}
}

// BuiltinTranslationPromptTemplates 返回所有内置翻译提示词模板（当前仅一个）。
func BuiltinTranslationPromptTemplates() []*ent.TranslationPromptTemplate {
	return []*ent.TranslationPromptTemplate{builtinTranslationPromptTemplate}
}

// BuiltinTranslationPromptTemplate 根据 id 返回内置翻译提示词模板，id 不匹配时返回 nil。
func BuiltinTranslationPromptTemplate(id int) *ent.TranslationPromptTemplate {
	if id == BuiltinTranslationPromptTemplateID {
		return builtinTranslationPromptTemplate
	}
	return nil
}

// ── 内置 BootstrapPromptTemplate 虚拟实体 ───────────────────

var builtinBootstrapPromptTemplate *ent.BootstrapPromptTemplate

func init() {
	meta := parseBuiltinConfig()
	builtinBootstrapPromptTemplate = &ent.BootstrapPromptTemplate{
		ID:          BuiltinBootstrapPromptTemplateID,
		Name:        meta.BootstrapPromptTemplate.Name,
		Description: meta.BootstrapPromptTemplate.Description,
		Scope:       "system",
		Content:     EmbeddedBootstrapTemplate(),
	}
}

// BuiltinBootstrapPromptTemplates 返回所有内置术语抽取提示词模板（当前仅一个）。
func BuiltinBootstrapPromptTemplates() []*ent.BootstrapPromptTemplate {
	return []*ent.BootstrapPromptTemplate{builtinBootstrapPromptTemplate}
}

// BuiltinBootstrapPromptTemplate 根据 id 返回内置术语抽取提示词模板，id 不匹配时返回 nil。
func BuiltinBootstrapPromptTemplate(id int) *ent.BootstrapPromptTemplate {
	if id == BuiltinBootstrapPromptTemplateID {
		return builtinBootstrapPromptTemplate
	}
	return nil
}

// ── 内置 PrunePromptTemplate 虚拟实体 ───────────────────────

var builtinPrunePromptTemplate *ent.PrunePromptTemplate

func init() {
	meta := parseBuiltinConfig()
	builtinPrunePromptTemplate = &ent.PrunePromptTemplate{
		ID:          BuiltinPrunePromptTemplateID,
		Name:        meta.PrunePromptTemplate.Name,
		Description: meta.PrunePromptTemplate.Description,
		Scope:       "system",
		Content:     EmbeddedPruneTemplate(),
	}
}

// BuiltinPrunePromptTemplates 返回所有内置术语精简提示词模板（当前仅一个）。
func BuiltinPrunePromptTemplates() []*ent.PrunePromptTemplate {
	return []*ent.PrunePromptTemplate{builtinPrunePromptTemplate}
}

// BuiltinPrunePromptTemplate 根据 id 返回内置术语精简提示词模板，id 不匹配时返回 nil。
func BuiltinPrunePromptTemplate(id int) *ent.PrunePromptTemplate {
	if id == BuiltinPrunePromptTemplateID {
		return builtinPrunePromptTemplate
	}
	return nil
}

// ── 内置 ExecutionProfile 虚拟实体 ───────────────────────────

var builtinProfileConfig schema.ExecutionProfileConfigData
var builtinExecutionProfile *ent.ExecutionProfile

func init() {
	if err := yaml.Unmarshal(EmbeddedProfileConfig(), &builtinProfileConfig); err != nil {
		panic(fmt.Sprintf("failed to parse embedded profile config: %v", err))
	}
}

func init() {
	meta := parseBuiltinConfig()
	builtinExecutionProfile = &ent.ExecutionProfile{
		ID:          BuiltinExecutionProfileID,
		Name:        meta.TranslationProfile.Name,
		Description: meta.TranslationProfile.Description,
		Scope:       "system",
		Config:      builtinProfileConfig,
	}
}

// BuiltinExecutionProfiles 返回所有内置执行策略（当前仅一个）。
func BuiltinExecutionProfiles() []*ent.ExecutionProfile {
	return []*ent.ExecutionProfile{builtinExecutionProfile}
}

// BuiltinExecutionProfile 根据 id 返回内置执行策略，id 不匹配时返回 nil。
func BuiltinExecutionProfile(id int) *ent.ExecutionProfile {
	if id == BuiltinExecutionProfileID {
		return builtinExecutionProfile
	}
	return nil
}
