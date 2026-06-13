package service

import (
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"gopkg.in/yaml.v3"
)

// MergeTranslationConfig 合并翻译配置。
// 优先级：jobConfig > projectConfig > global。
func MergeTranslationConfig(global *config.Config, projectConfig map[string]any, jobConfig map[string]any) (*config.Config, error) {
	if global == nil {
		return nil, fmt.Errorf("translation config: nil global config")
	}
	merged := CloneConfig(global)
	if err := applyTranslationConfig(merged, projectConfig); err != nil {
		return nil, fmt.Errorf("translation config: apply project config: %w", err)
	}
	if err := applyTranslationConfig(merged, jobConfig); err != nil {
		return nil, fmt.Errorf("translation config: apply job config: %w", err)
	}
	return merged, nil
}

func mergeConfigMaps(base map[string]any, override map[string]any) map[string]any {
	merged := cloneMap(base)
	for key, value := range override {
		if baseValue, ok := merged[key].(map[string]any); ok {
			if overrideValue, ok := value.(map[string]any); ok {
				merged[key] = mergeConfigMaps(baseValue, overrideValue)
				continue
			}
		}
		merged[key] = value
	}
	return merged
}

func applyTranslationConfig(cfg *config.Config, override map[string]any) error {
	if cfg == nil || len(override) == 0 {
		return nil
	}
	normalized := normalizeTranslationOverride(override)
	raw, err := yaml.Marshal(normalized)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return err
	}
	return nil
}

func normalizeTranslationOverride(in map[string]any) map[string]any {
	out := cloneMap(in)
	translateKeys := []string{
		"concurrency",
		"batch_size",
		"fallback_shrink",
		"rate_limit_per_sec",
		"backend_mode",
		"backend_order",
		"plan",
		"retry",
		"repair",
	}
	var moved map[string]any
	for _, key := range translateKeys {
		value, ok := out[key]
		if !ok {
			continue
		}
		if moved == nil {
			moved = map[string]any{}
		}
		moved[key] = value
		delete(out, key)
	}
	if len(moved) > 0 {
		pipeline := ensureMap(out, "pipeline")
		translate := ensureMap(pipeline, "translate")
		for key, value := range moved {
			translate[key] = value
		}
	}
	return out
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	if existing, ok := parent[key].(map[any]any); ok {
		for k, v := range existing {
			if s, ok := k.(string); ok {
				created[s] = v
			}
		}
	}
	parent[key] = created
	return created
}

// CloneConfig 深拷贝配置中运行时会修改的切片与 map 字段。
func CloneConfig(in *config.Config) *config.Config {
	if in == nil {
		return nil
	}
	copyCfg := *in
	copyCfg.Backends = make([]config.BackendConfig, 0, len(in.Backends))
	for _, backendCfg := range in.Backends {
		backendCopy := backendCfg
		backendCopy.Options = cloneMap(backendCfg.Options)
		copyCfg.Backends = append(copyCfg.Backends, backendCopy)
	}
	copyCfg.Prompt.Vars = cloneMap(in.Prompt.Vars)
	copyCfg.Pipeline.Protect.Rules = append([]string(nil), in.Pipeline.Protect.Rules...)
	copyCfg.Plugins.Scripts = append([]string(nil), in.Plugins.Scripts...)
	copyCfg.Server.CORS.AllowedOrigins = append([]string(nil), in.Server.CORS.AllowedOrigins...)
	return &copyCfg
}
