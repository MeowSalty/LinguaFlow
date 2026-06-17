package cli

import (
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// applyTranslateFlags 把 CLI 覆盖应用到 CLIConfig。
//
// glossary-path 非空：cliCfg.Glossary.Path 改写、Enabled 强制 true。
// bootstrap 非空：校验取值，覆盖 cliCfg.Glossary.Bootstrap.Mode；
// 非 "off" 时一并把 Glossary.Enabled 设为 true（与 config.Validate 一致）。
// profile 非空：将所有执行轮次的 profile 替换为指定值。
// prompt 非空：将所有执行轮次的 prompt 替换为指定值。
func applyTranslateFlags(cliCfg *config.CLIConfig, opts translateOptions) error {
	if opts.glossaryPath != "" {
		cliCfg.Glossary.Path = opts.glossaryPath
		cliCfg.Glossary.Enabled = true
	}
	if opts.bootstrapMode != "" {
		switch opts.bootstrapMode {
		case config.BootstrapModeOff, config.BootstrapModePre, config.BootstrapModeInline:
		default:
			return fmt.Errorf("--bootstrap must be one of off|pre|inline, got %q", opts.bootstrapMode)
		}
		cliCfg.Glossary.Bootstrap.Mode = opts.bootstrapMode
		if opts.bootstrapMode != config.BootstrapModeOff {
			cliCfg.Glossary.Enabled = true
		}
	}
	// profile 覆盖：将所有执行轮次的 profile 替换为指定值
	if opts.profile != "" {
		if _, ok := cliCfg.TranslationProfiles[opts.profile]; !ok {
			return fmt.Errorf("translation profile %q not found", opts.profile)
		}
		for i := range cliCfg.Execution.Rounds {
			cliCfg.Execution.Rounds[i].Profile = opts.profile
		}
	}
	// prompt 覆盖：将所有执行轮次的 prompt 替换为指定值
	if opts.prompt != "" {
		if _, ok := cliCfg.PromptTemplates[opts.prompt]; !ok {
			return fmt.Errorf("prompt template %q not found", opts.prompt)
		}
		for i := range cliCfg.Execution.Rounds {
			cliCfg.Execution.Rounds[i].Prompt = opts.prompt
		}
	}
	return nil
}
