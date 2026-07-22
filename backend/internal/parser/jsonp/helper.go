package jsonp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// normalizeFormat 归一化格式提示：去前导点、小写、yml→yaml；空串保持空。
func normalizeFormat(format string) string {
	format = strings.TrimSpace(format)
	format = strings.TrimPrefix(format, ".")
	format = strings.ToLower(format)
	if format == "yml" {
		return "yaml"
	}
	return format
}

// detectFormat 根据内容试解析检测结构化格式（仅在无 format 提示时兜底）。
func detectFormat(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "json"
	}

	first := trimmed[0]
	switch {
	case first == '{':
		return "json"
	case first == '[':
		// JSON 数组 vs TOML [table]
		var j any
		if json.Unmarshal([]byte(trimmed), &j) == nil {
			return "json"
		}
		var t any
		if toml.Unmarshal([]byte(trimmed), &t) == nil {
			return "toml"
		}
		return "yaml"
	case first == '#':
		// TOML #comment vs YAML #comment
		var t any
		if toml.Unmarshal([]byte(trimmed), &t) == nil {
			return "toml"
		}
		return "yaml"
	default:
		// key = value 形似 TOML；key: value 形似 YAML
		if preferTOMLByEquals(trimmed) {
			var t any
			if toml.Unmarshal([]byte(trimmed), &t) == nil {
				return "toml"
			}
		}
		var y any
		if yaml.Unmarshal([]byte(trimmed), &y) == nil {
			return "yaml"
		}
		var t any
		if toml.Unmarshal([]byte(trimmed), &t) == nil {
			return "toml"
		}
		return "yaml"
	}
}

// preferTOMLByEquals 若存在含 = 且 = 前无 : 的行，优先尝试 TOML。
func preferTOMLByEquals(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		if strings.IndexByte(line[:eq], ':') >= 0 {
			continue
		}
		return true
	}
	return false
}

// collectStrings 递归遍历树，收集所有非空字符串值为 Segment。
// path 是当前节点的点号路径（如 "config.title" 或 "items[0].name"）。
func collectStrings(v any, path string, segments *[]pipeline.Segment) {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			collectStrings(child, childPath, segments)
		}
	case []any:
		for i, child := range val {
			childPath := path + "[" + strconv.Itoa(i) + "]"
			collectStrings(child, childPath, segments)
		}
	case string:
		if strings.TrimSpace(val) == "" {
			return // 跳过空字符串
		}
		*segments = append(*segments, pipeline.Segment{
			ID:     shortHash(val),
			Source: val,
			Meta: map[string]any{
				"path": path,
			},
		})
	}
	// 其他类型（number, bool, nil, time.Time）不可翻译，忽略
}

// buildTranslatedTree 递归构建翻译后的新树，不修改原始树。
func buildTranslatedTree(v any, path string, lookup map[string]string) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any, len(val))
		for k, child := range val {
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			result[k] = buildTranslatedTree(child, childPath, lookup)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, child := range val {
			childPath := path + "[" + strconv.Itoa(i) + "]"
			result[i] = buildTranslatedTree(child, childPath, lookup)
		}
		return result
	case string:
		if translated, ok := lookup[path]; ok {
			return translated
		}
		return val
	default:
		// number, bool, nil, time.Time — 原样返回
		return val
	}
}

// shortHash 生成字符串的短 hash（12 位 hex），用作 Segment ID。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}
