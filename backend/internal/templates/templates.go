// Package templates 加载并提供内置模板集合。
// 从嵌入的 default/ 目录读取 config.yaml 和 prompt.tmpl。
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"gopkg.in/yaml.v3"
)

// PromptTemplate 内置提示词模板。
type PromptTemplate struct {
	ID                  int
	Name                string
	Description         string
	SystemPromptContent string
}

// TranslationProfile 内置翻译配置。
type TranslationProfile struct {
	ID          int
	Name        string
	Description string
	Config      schema.TranslationProfileConfigData
}

// Backend 内置后端配置。
type Backend struct {
	Name    string         `yaml:"name"`
	Type    string         `yaml:"type"`
	Options map[string]any `yaml:"options"`
}

// ExecutionPlan 内置执行计划（仅 CLI 使用）。
type ExecutionPlan struct {
	Name        string
	Description string
	Rounds      []ExecutionRound
}

// ExecutionRound 内置执行轮次。
type ExecutionRound struct {
	Name            string
	Backend         string // 按名称引用
	PromptTemplate  string // 已解析为名称
	Profile         string // 已解析为名称
	BatchSize       int
	Concurrency     int
	FallbackShrink  float64
	RateLimitPerSec int
	Retry           schema.RetryConfig
}

// Set 一组关联的内置 PromptTemplate + TranslationProfile。
type Set struct {
	PromptTemplate     PromptTemplate
	TranslationProfile TranslationProfile
	Backend            *Backend       // 可选，仅 CLI 使用
	ExecutionPlan      *ExecutionPlan // 可选，仅 CLI 使用
}

// setFile 是 config.yaml 的反序列化结构。
type setFile struct {
	Backend *struct {
		Name    string         `yaml:"name"`
		Type    string         `yaml:"type"`
		Options map[string]any `yaml:"options"`
	} `yaml:"backend"`
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
	ExecutionPlan *struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Rounds      []struct {
			Name            string             `yaml:"name"`
			Backend         string             `yaml:"backend"`
			PromptTemplate  int                `yaml:"prompt_template"`
			Profile         int                `yaml:"profile"`
			BatchSize       int                `yaml:"batch_size"`
			Concurrency     int                `yaml:"concurrency"`
			FallbackShrink  float64            `yaml:"fallback_shrink"`
			RateLimitPerSec int                `yaml:"rate_limit_per_sec"`
			Retry           schema.RetryConfig `yaml:"retry"`
		} `yaml:"rounds"`
	} `yaml:"execution_plan"`
}

//go:embed default
var builtinFS embed.FS

// BuiltinSets 返回所有内置模板集合列表。
// 启动时从嵌入的 default/ 目录加载。
var BuiltinSets []Set

func init() {
	sets, err := loadSets()
	if err != nil {
		panic(fmt.Sprintf("failed to load builtin templates: %v", err))
	}
	BuiltinSets = sets
}

// loadSets 遍历嵌入的 default/ 目录，
// 从每个子目录中读取 config.yaml 和 prompt.tmpl 构建 Set。
func loadSets() ([]Set, error) {
	// 收集所有子目录名
	dirs, err := collectSubdirs(".")
	if err != nil {
		return nil, fmt.Errorf("collect subdirs: %w", err)
	}

	sets := make([]Set, 0, len(dirs))
	for _, dir := range dirs {
		basePath := dir

		// 读取 config.yaml
		cfgData, err := fs.ReadFile(builtinFS, basePath+"/config.yaml")
		if err != nil {
			return nil, fmt.Errorf("read %s/config.yaml: %w", dir, err)
		}

		var cfg setFile
		if err := yaml.Unmarshal(cfgData, &cfg); err != nil {
			return nil, fmt.Errorf("parse %s/config.yaml: %w", dir, err)
		}

		// 读取 prompt.tmpl
		promptData, err := fs.ReadFile(builtinFS, basePath+"/prompt.tmpl")
		if err != nil {
			return nil, fmt.Errorf("read %s/prompt.tmpl: %w", dir, err)
		}

		set := Set{
			PromptTemplate: PromptTemplate{
				ID:                  cfg.PromptTemplate.ID,
				Name:                cfg.PromptTemplate.Name,
				Description:         cfg.PromptTemplate.Description,
				SystemPromptContent: strings.TrimRight(string(promptData), "\n"),
			},
			TranslationProfile: TranslationProfile{
				ID:          cfg.TranslationProfile.ID,
				Name:        cfg.TranslationProfile.Name,
				Description: cfg.TranslationProfile.Description,
				Config:      cfg.TranslationProfile.Config,
			},
		}

		// Backend 和 ExecutionPlan 为可选字段
		if cfg.Backend != nil {
			set.Backend = &Backend{
				Name:    cfg.Backend.Name,
				Type:    cfg.Backend.Type,
				Options: cfg.Backend.Options,
			}
		}
		if cfg.ExecutionPlan != nil {
			rounds := make([]ExecutionRound, 0, len(cfg.ExecutionPlan.Rounds))
			for _, raw := range cfg.ExecutionPlan.Rounds {
				rounds = append(rounds, buildRound(raw, &cfg))
			}
			set.ExecutionPlan = &ExecutionPlan{
				Name:        cfg.ExecutionPlan.Name,
				Description: cfg.ExecutionPlan.Description,
				Rounds:      rounds,
			}
		}

		sets = append(sets, set)
	}

	return sets, nil
}

// buildRound 将反序列化的轮次转换为 ExecutionRound，
// 并将 prompt_template/profile 的内置 ID 解析为对应的名称。
func buildRound(raw struct {
	Name            string             `yaml:"name"`
	Backend         string             `yaml:"backend"`
	PromptTemplate  int                `yaml:"prompt_template"`
	Profile         int                `yaml:"profile"`
	BatchSize       int                `yaml:"batch_size"`
	Concurrency     int                `yaml:"concurrency"`
	FallbackShrink  float64            `yaml:"fallback_shrink"`
	RateLimitPerSec int                `yaml:"rate_limit_per_sec"`
	Retry           schema.RetryConfig `yaml:"retry"`
}, cfg *setFile) ExecutionRound {
	promptName := cfg.PromptTemplate.Name
	profileName := cfg.TranslationProfile.Name

	return ExecutionRound{
		Name:            raw.Name,
		Backend:         raw.Backend,
		PromptTemplate:  promptName,
		Profile:         profileName,
		BatchSize:       raw.BatchSize,
		Concurrency:     raw.Concurrency,
		FallbackShrink:  raw.FallbackShrink,
		RateLimitPerSec: raw.RateLimitPerSec,
		Retry:           raw.Retry,
	}
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
