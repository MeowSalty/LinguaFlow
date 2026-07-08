package config

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// envVarPattern 匹配 ${NAME} 或 ${NAME:-default} 形式的占位符。
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// LoadServerConfig 从环境变量加载服务器配置，并与默认值合并。
// 环境变量优先级高于默认值，CLI flag 通过 Overrides 回调在调用方覆盖。
// 优先级：CLI flag > 环境变量 > 内置默认值。
func LoadServerConfig() (*ServerConfig, error) {
	cfg := DefaultServerConfig()
	applyServerEnvOverrides(cfg)
	if err := ValidateServerConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyServerEnvOverrides 从环境变量读取服务器配置并覆盖默认值。
// 环境变量前缀为 LINGUAFLOW_，仅在显式设置时覆盖。
func applyServerEnvOverrides(cfg *ServerConfig) {
	if v := os.Getenv("LINGUAFLOW_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("LINGUAFLOW_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Port = port
		}
	}
	if v := os.Getenv("LINGUAFLOW_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("LINGUAFLOW_SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	if v := os.Getenv("LINGUAFLOW_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("LINGUAFLOW_JWT_ISSUER"); v != "" {
		cfg.JWTIssuer = v
	}
	if v := os.Getenv("LINGUAFLOW_JWT_EXPIRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.JWTExpiry = d
		}
	}
	if v := os.Getenv("LINGUAFLOW_REFRESH_TOKEN_EXPIRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RefreshExpiry = d
		}
	}
	if v := os.Getenv("LINGUAFLOW_SHUTDOWN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ShutdownTimeout = d
		}
	}
	if v := os.Getenv("LINGUAFLOW_CORS_ORIGINS"); v != "" {
		cfg.CORS.AllowedOrigins = strings.Split(v, ",")
	}
	if v := os.Getenv("LINGUAFLOW_REGISTRATION_ENABLED"); v != "" {
		cfg.Registration.Enabled = parseBool(v)
	}
	if v := os.Getenv("LINGUAFLOW_REGISTRATION_AUTO_ADMIN"); v != "" {
		cfg.Registration.AutoAdmin = parseBool(v)
	}
}

func parseBool(v string) bool {
	return v == "true" || v == "1" || v == "yes"
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
