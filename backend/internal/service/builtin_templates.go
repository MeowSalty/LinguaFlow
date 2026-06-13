package service

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"gopkg.in/yaml.v3"
)

// BuiltinPromptTemplate 内置提示词模板。
type BuiltinPromptTemplate struct {
	ID                  int
	Name                string
	Description         string
	SystemPromptContent string
}

// BuiltinTranslationProfile 内置翻译配置。
type BuiltinTranslationProfile struct {
	ID          int
	Name        string
	Description string
	Config      schema.TranslationProfileConfigData
}

// BuiltinSet 一组关联的内置 PromptTemplate + TranslationProfile。
type BuiltinSet struct {
	PromptTemplate     BuiltinPromptTemplate
	TranslationProfile BuiltinTranslationProfile
}

// builtinSetFile 是 config.yaml 的反序列化结构。
type builtinSetFile struct {
	PromptTemplate struct {
		ID          int    `yaml:"id"`
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	} `yaml:"prompt_template"`
	TranslationProfile struct {
		ID          int                                 `yaml:"id"`
		Name        string                              `yaml:"name"`
		Description string                              `yaml:"description"`
		Config      schema.TranslationProfileConfigData `yaml:"config"`
	} `yaml:"translation_profile"`
}

//go:embed builtin_templates
var builtinFS embed.FS

// BuiltinSets 返回所有内置模板集合列表。
// 启动时从嵌入的 builtin_templates/ 目录加载。
var BuiltinSets []BuiltinSet

func init() {
	sets, err := loadBuiltinSets()
	if err != nil {
		panic(fmt.Sprintf("failed to load builtin templates: %v", err))
	}
	BuiltinSets = sets
}

// loadBuiltinSets 遍历嵌入的 builtin_templates/ 目录，
// 从每个子目录中读取 config.yaml 和 prompt.tmpl 构建 BuiltinSet。
func loadBuiltinSets() ([]BuiltinSet, error) {
	// 收集所有子目录名
	dirs, err := collectSubdirs("builtin_templates")
	if err != nil {
		return nil, fmt.Errorf("collect subdirs: %w", err)
	}

	sets := make([]BuiltinSet, 0, len(dirs))
	for _, dir := range dirs {
		basePath := "builtin_templates/" + dir

		// 读取 config.yaml
		cfgData, err := fs.ReadFile(builtinFS, basePath+"/config.yaml")
		if err != nil {
			return nil, fmt.Errorf("read %s/config.yaml: %w", dir, err)
		}

		var cfg builtinSetFile
		if err := yaml.Unmarshal(cfgData, &cfg); err != nil {
			return nil, fmt.Errorf("parse %s/config.yaml: %w", dir, err)
		}

		// 读取 prompt.tmpl
		promptData, err := fs.ReadFile(builtinFS, basePath+"/prompt.tmpl")
		if err != nil {
			return nil, fmt.Errorf("read %s/prompt.tmpl: %w", dir, err)
		}

		sets = append(sets, BuiltinSet{
			PromptTemplate: BuiltinPromptTemplate{
				ID:                  cfg.PromptTemplate.ID,
				Name:                cfg.PromptTemplate.Name,
				Description:         cfg.PromptTemplate.Description,
				SystemPromptContent: strings.TrimRight(string(promptData), "\n"),
			},
			TranslationProfile: BuiltinTranslationProfile{
				ID:          cfg.TranslationProfile.ID,
				Name:        cfg.TranslationProfile.Name,
				Description: cfg.TranslationProfile.Description,
				Config:      cfg.TranslationProfile.Config,
			},
		})
	}

	return sets, nil
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

// FindBuiltinPromptTemplate 根据 ID 查找内置提示词模板，返回 nil 表示非内置。
func FindBuiltinPromptTemplate(id int) *BuiltinPromptTemplate {
	for i := range BuiltinSets {
		if BuiltinSets[i].PromptTemplate.ID == id {
			return &BuiltinSets[i].PromptTemplate
		}
	}
	return nil
}

// FindBuiltinTranslationProfile 根据 ID 查找内置翻译配置，返回 nil 表示非内置。
func FindBuiltinTranslationProfile(id int) *BuiltinTranslationProfile {
	for i := range BuiltinSets {
		if BuiltinSets[i].TranslationProfile.ID == id {
			return &BuiltinSets[i].TranslationProfile
		}
	}
	return nil
}
