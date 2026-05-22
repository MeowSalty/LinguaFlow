package glossary

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// New 按配置构造 Glossary。cfg.Enabled=false 时返回 Nop；否则按扩展名分发，
// 目前仅支持 .csv。
func New(cfg config.GlossaryConfig) (Glossary, error) {
	if !cfg.Enabled {
		return Nop{}, nil
	}
	if cfg.Path == "" {
		return nil, fmt.Errorf("glossary: enabled but path is empty")
	}
	ext := strings.ToLower(filepath.Ext(cfg.Path))
	switch ext {
	case ".csv":
		return LoadFile(cfg.Path)
	default:
		return nil, fmt.Errorf("glossary: unsupported format %q (only .csv is supported)", ext)
	}
}
