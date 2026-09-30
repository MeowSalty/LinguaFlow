package config

import "os"

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
