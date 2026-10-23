package config

import (
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EncodeServerDocument saves resolved deployment inputs using the same explicit
// field catalog as the loader. The caller must publish these secret bytes with
// private permissions. Bootstrap inputs never belong in a recovery deployment.
func EncodeServerDocument(cfg ServerConfig, log LogConfig) ([]byte, error) {
	r := &ResolvedServer{Config: cfg, Log: log}
	root := map[string]any{"kind": "server", "version": 1}
	for _, f := range serverFields {
		if strings.HasPrefix(f.Key, "bootstrap.") || f.Key == "server.credentials.master_key" || cfg.IsLocal() && f.Modes == "serve" {
			continue
		}
		value := f.read(r)
		if f.Type == "nullable_quota" {
			quota := value.(QuotaInput)
			if !quota.Set {
				continue
			}
			value = quota.Value
		}
		if duration, ok := value.(time.Duration); ok {
			value = duration.String()
		}
		if list, ok := value.([]string); ok && list == nil {
			value = []string{}
		}
		if f.Type == "storage_backends" && len(cfg.Storage.Backends) == 0 {
			continue
		}
		if f.Sensitive && value == "" {
			continue
		}
		parts := strings.Split(f.Key, ".")
		object := root
		for _, part := range parts[:len(parts)-1] {
			child, ok := object[part].(map[string]any)
			if !ok {
				child = map[string]any{}
				object[part] = child
			}
			object = child
		}
		object[parts[len(parts)-1]] = value
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		return nil, err
	}
	mode := ModeServer
	if cfg.IsLocal() {
		mode = ModeLocal
	}
	if err := decodeServerDocument(data, "recovery.yml", map[string]string{}, mode, map[string]sourcedValue{}); err != nil {
		return nil, err
	}
	return data, nil
}
