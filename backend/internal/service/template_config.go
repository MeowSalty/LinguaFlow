package service

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
)

// TemplateData 持有模板的完整配置。
// 所有配置（Prompt/Pipeline/Glossary）均内联在 TranslationTemplate 主表中。
type TemplateData struct {
	Template *ent.TranslationTemplate // 主表，包含所有内联配置
}

// PipelineToConfig 将 JSON 管线配置转换为 config.PipelineConfig。
func PipelineToConfig(p *schema.TemplatePipelineConfigData) config.PipelineConfig {
	return config.PipelineConfig{
		Split: config.SplitConfig{
			Enabled:  p.Split.Enabled,
			Strategy: p.Split.Strategy,
			MaxChars: p.Split.MaxChars,
		},
		Protect: config.ProtectConfig{
			Enabled: p.Protect.Enabled,
			Rules:   p.Protect.Rules,
		},
		// Translate 中的批次/并发/限流字段不在模板中，保留全局默认
		Translate: config.TranslateConfig{
			Retry: config.RetryConfig{
				MaxAttempts: p.Retry.MaxAttempts,
				BackoffMs:   p.Retry.BackoffMs,
				Jitter:      p.Retry.Jitter,
			},
			Repair: config.RepairConfig{
				Enabled:              p.Repair.Enabled,
				JSONStructural:       p.Repair.JSONStructural,
				SchemaAliases:        p.Repair.SchemaAliases,
				Partial:              p.Repair.Partial,
				PartialThreshold:     p.Repair.PartialThreshold,
				PlaceholderNormalize: p.Repair.PlaceholderNormalize,
				PromptUpgrade:        p.Repair.PromptUpgrade,
			},
		},
		Postprocess: config.PostprocessConfig{
			Enabled:    p.Postprocess.Enabled,
			TrimSpaces: p.Postprocess.TrimSpaces,
		},
	}
}

// GlossaryToConfig 将 JSON 术语表配置转换为 config.GlossaryConfig。
func GlossaryToConfig(g *schema.TemplateGlossaryConfigData) config.GlossaryConfig {
	return config.GlossaryConfig{
		Enabled: g.Enabled,
		Bootstrap: config.BootstrapConfig{
			Mode:                   g.Bootstrap.Mode,
			Save:                   g.Bootstrap.Save,
			MaxTermsPerBatch:       g.Bootstrap.MaxTermsPerBatch,
			MinSourceLen:           g.Bootstrap.MinSourceLen,
			InlineConflictStrategy: g.Bootstrap.InlineConflictStrategy,
		},
	}
}

// TemplateToConfig 从模板配置构建完整的 config.Config。
// 这是 Web 路径的唯一配置构建方式，不涉及 map[string]any 合并。
func TemplateToConfig(
	global *config.Config, // 仅用于 Server/Log 等运行时配置
	data TemplateData,
	sourceLang string,
	targetLang string,
) (*config.Config, error) {
	cfg := CloneConfig(global)

	// 语言对来自项目
	cfg.SourceLang = sourceLang
	cfg.TargetLang = targetLang

	// Prompt：从主表内联字段直接赋值（风格/受众等要求已写在提示词文本中）
	if data.Template.SystemPromptContent != "" {
		cfg.Prompt.SystemTemplateContent = data.Template.SystemPromptContent
	}

	// Pipeline：从内联 JSON 字段直接转换
	cfg.Pipeline = PipelineToConfig(&data.Template.PipelineConfig)

	// Glossary：从内联 JSON 字段直接转换
	cfg.Glossary = GlossaryToConfig(&data.Template.GlossaryConfig)

	return cfg, nil
}
