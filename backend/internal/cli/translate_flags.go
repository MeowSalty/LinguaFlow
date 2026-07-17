package cli

import (
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// applyTranslateFlags 把 CLI 覆盖应用到 CLIConfig。
//
// glossary-path 非空：cliCfg.Glossary.Path 改写、Enabled 强制 true。
// bootstrap 非空：校验取值，覆盖独立自举开关；
// 非 "off" 时一并把 Glossary.Enabled 设为 true（与 config.Validate 一致）。
// profile 非空：将所有翻译轮次的 profile 替换为指定值。
// prompt 非空：将所有翻译轮次的 prompt 替换为指定值。
func applyTranslateFlags(cliCfg *config.CLIConfig, opts translateOptions) error {
	if opts.glossaryPath != "" {
		cliCfg.Glossary.Path = opts.glossaryPath
		cliCfg.Glossary.Enabled = true
	}
	if opts.bootstrapMode != "" {
		switch opts.bootstrapMode {
		case config.BootstrapModeOff:
			// 移除所有 extract 轮次
			var filtered []config.CLIConfigRound
			for _, r := range cliCfg.Execution.Rounds {
				if r.Mode != "extract" {
					filtered = append(filtered, r)
				}
			}
			cliCfg.Execution.Rounds = filtered
		case config.BootstrapModePre:
			// 确保存在 extract 轮次
			hasExtract := false
			for _, r := range cliCfg.Execution.Rounds {
				if r.Mode == "extract" {
					hasExtract = true
					break
				}
			}
			if !hasExtract {
				// 在最前面插入一个 extract 轮次
				extractRound := config.CLIConfigRound{
					Mode:    "extract",
					Name:    "术语抽取",
					Backend: "openai-default",
					Extract: &config.CLIConfigExtractRound{
						Template:             "default",
						BatchSize:            20,
						Concurrency:          2,
						MaxTermsPer1000Chars: 25.0,
						MinSourceLen:         2,
					},
				}
				cliCfg.Execution.Rounds = append([]config.CLIConfigRound{extractRound}, cliCfg.Execution.Rounds...)
			}
			cliCfg.Glossary.Enabled = true
		case config.BootstrapModeInline:
			// inline 模式由 Profile 配置控制，CLI flag 仅开启术语表
			cliCfg.Glossary.Enabled = true
		default:
			return fmt.Errorf("--bootstrap must be one of off|pre|inline, got %q", opts.bootstrapMode)
		}
	}
	// profile 覆盖：将所有翻译轮次的 profile 替换为指定值
	if opts.profile != "" {
		if _, ok := cliCfg.TranslationProfiles[opts.profile]; !ok {
			return fmt.Errorf("translation profile %q not found", opts.profile)
		}
		for i := range cliCfg.Execution.Rounds {
			if cliCfg.Execution.Rounds[i].Mode == "translate" && cliCfg.Execution.Rounds[i].Translate != nil {
				cliCfg.Execution.Rounds[i].Translate.Profile = opts.profile
			}
		}
	}
	// prompt 覆盖：将所有翻译轮次的 prompt 替换为指定值
	if opts.prompt != "" {
		if _, ok := cliCfg.PromptTemplates[opts.prompt]; !ok {
			return fmt.Errorf("prompt template %q not found", opts.prompt)
		}
		for i := range cliCfg.Execution.Rounds {
			if cliCfg.Execution.Rounds[i].Mode == "translate" && cliCfg.Execution.Rounds[i].Translate != nil {
				cliCfg.Execution.Rounds[i].Translate.Prompt = opts.prompt
			}
		}
	}
	return nil
}
