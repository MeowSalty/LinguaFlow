package cli

import (
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// applyTranslateFlags 把 CLI 覆盖应用到 cfg。
//
// glossary-path 非空：cfg.Glossary.Path 改写、Enabled 强制 true。
// bootstrap 非空：校验取值，覆盖 cfg.Glossary.Bootstrap.Mode；
// 非 "off" 时一并把 Glossary.Enabled 设为 true（与 config.Validate 一致）。
func applyTranslateFlags(cfg *config.Config, glossaryPath, bootstrapMode string) error {
	if glossaryPath != "" {
		cfg.Glossary.Path = glossaryPath
		cfg.Glossary.Enabled = true
	}
	if bootstrapMode != "" {
		switch bootstrapMode {
		case config.BootstrapModeOff, config.BootstrapModePre, config.BootstrapModeInline:
		default:
			return fmt.Errorf("--bootstrap must be one of off|pre|inline, got %q", bootstrapMode)
		}
		cfg.Glossary.Bootstrap.Mode = bootstrapMode
		if bootstrapMode != config.BootstrapModeOff {
			cfg.Glossary.Enabled = true
		}
	}
	return nil
}
