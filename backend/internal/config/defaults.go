package config

import (
	_ "embed"
)

// DefaultYAML 是 `linguaflow init` 写出的带注释的配置内容。
// 与仓库根目录 linguaflow.example.yaml 保持一致。
//
//go:embed defaults.yaml
var DefaultYAML []byte
