package service

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"gopkg.in/yaml.v3"
)

// BuiltinTemplate 运行时内置模板，不持久化到数据库。
type BuiltinTemplate struct {
	ID          int
	Name        string
	Description string
	Scope       string // 始终为 "system"
	Prompt      BuiltinPromptConfig
	Pipeline    schema.TemplatePipelineConfigData
	Glossary    schema.TemplateGlossaryConfigData
}

// BuiltinPromptConfig 内置模板的提示词配置。
type BuiltinPromptConfig struct {
	SystemPromptContent string // 风格/受众等翻译要求直接写在提示词中
}

// builtinConfigFile 是 config.yaml 的反序列化结构。
type builtinConfigFile struct {
	ID          int                               `yaml:"id"`
	Name        string                            `yaml:"name"`
	Description string                            `yaml:"description"`
	Scope       string                            `yaml:"scope"`
	Pipeline    schema.TemplatePipelineConfigData `yaml:"pipeline"`
	Glossary    schema.TemplateGlossaryConfigData `yaml:"glossary"`
}

//go:embed builtin_templates
var builtinFS embed.FS

// BuiltinTemplates 返回所有内置模板列表。
// 启动时从嵌入的 builtin_templates/ 目录加载。
var BuiltinTemplates []BuiltinTemplate

func init() {
	templates, err := loadBuiltinTemplates()
	if err != nil {
		panic(fmt.Sprintf("failed to load builtin templates: %v", err))
	}
	BuiltinTemplates = templates
}

// loadBuiltinTemplates 遍历嵌入的 builtin_templates/ 目录，
// 从每个子目录中读取 config.yaml 和 prompt.tmpl 构建 BuiltinTemplate。
func loadBuiltinTemplates() ([]BuiltinTemplate, error) {
	// 收集所有子目录名
	dirs, err := collectSubdirs("builtin_templates")
	if err != nil {
		return nil, fmt.Errorf("collect subdirs: %w", err)
	}

	templates := make([]BuiltinTemplate, 0, len(dirs))
	for _, dir := range dirs {
		basePath := "builtin_templates/" + dir

		// 读取 config.yaml
		cfgData, err := fs.ReadFile(builtinFS, basePath+"/config.yaml")
		if err != nil {
			return nil, fmt.Errorf("read %s/config.yaml: %w", dir, err)
		}

		var cfg builtinConfigFile
		if err := yaml.Unmarshal(cfgData, &cfg); err != nil {
			return nil, fmt.Errorf("parse %s/config.yaml: %w", dir, err)
		}

		// 读取 prompt.tmpl
		promptData, err := fs.ReadFile(builtinFS, basePath+"/prompt.tmpl")
		if err != nil {
			return nil, fmt.Errorf("read %s/prompt.tmpl: %w", dir, err)
		}

		templates = append(templates, BuiltinTemplate{
			ID:          cfg.ID,
			Name:        cfg.Name,
			Description: cfg.Description,
			Scope:       cfg.Scope,
			Prompt: BuiltinPromptConfig{
				SystemPromptContent: strings.TrimRight(string(promptData), "\n"),
			},
			Pipeline: cfg.Pipeline,
			Glossary: cfg.Glossary,
		})
	}

	return templates, nil
}

// collectSubdirs 列出嵌入文件系统中指定目录的所有一级子目录名。
func collectSubdirs(root string) ([]string, error) {
	var dirs []string
	entries, err := fs.ReadDir(builtinFS, root)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs, nil
}

// FindBuiltinTemplate 根据 ID 查找内置模板，返回 nil 表示非内置模板。
func FindBuiltinTemplate(id int) *BuiltinTemplate {
	for i := range BuiltinTemplates {
		if BuiltinTemplates[i].ID == id {
			return &BuiltinTemplates[i]
		}
	}
	return nil
}

// BuiltinTemplateToTemplateData 将内置模板转换为 TemplateData。
// 所有配置直接嵌入到构造的 TranslationTemplate 中。
func BuiltinTemplateToTemplateData(b *BuiltinTemplate) TemplateData {
	tmpl := &ent.TranslationTemplate{
		ID:                  b.ID,
		Name:                b.Name,
		Description:         b.Description,
		Scope:               b.Scope,
		SystemPromptContent: b.Prompt.SystemPromptContent,
		PipelineConfig:      b.Pipeline,
		GlossaryConfig:      b.Glossary,
	}
	return TemplateData{Template: tmpl}
}
