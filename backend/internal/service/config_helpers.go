package service

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
