package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// envVarPattern 匹配 ${NAME} 或 ${NAME:-default} 形式的占位符。
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// Load 从 path 读取 yaml 配置，并与默认值合并、展开 ${ENV} 占位符、调用 Validate。
// 若 path 为空字符串，则返回纯默认配置。
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	expanded := expandEnv(raw)

	// 在默认值之上反序列化 yaml，未指定的字段保留默认。
	if err := yaml.Unmarshal(expanded, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// expandEnv 把 yaml 中的 ${VAR} / ${VAR:-default} 替换为环境变量值。
func expandEnv(data []byte) []byte {
	return envVarPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		m := envVarPattern.FindSubmatch(match)
		name := string(m[1])
		def := ""
		if len(m) > 2 {
			def = string(m[2])
		}
		if v, ok := os.LookupEnv(name); ok {
			return []byte(v)
		}
		return []byte(def)
	})
}
