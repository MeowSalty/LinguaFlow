package glossary

import (
	"fmt"
	"path/filepath"
	"strings"
)

// New 按参数构造 Glossary。enabled=false 时返回 Nop；否则按扩展名分发，
// 目前仅支持 .csv。
func New(enabled bool, path string) (Glossary, error) {
	if !enabled {
		return Nop{}, nil
	}
	if path == "" {
		return nil, fmt.Errorf("glossary: enabled but path is empty")
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".csv":
		return LoadFile(path)
	default:
		return nil, fmt.Errorf("glossary: unsupported format %q (only .csv is supported)", ext)
	}
}
