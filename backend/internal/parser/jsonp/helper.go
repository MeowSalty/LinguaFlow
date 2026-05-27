package jsonp

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// detectFormat 根据内容前导字符检测结构化格式。
func detectFormat(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "json"
	}

	first := trimmed[0]
	switch {
	case first == '{' || first == '[':
		return "json"
	case first == '#' || first == '[':
		// TOML 可能以 #（注释）或 [table] 开头
		// YAML 也可以用 # 开头，此处保守返回 toml
		// 若解析失败会在 Parse 中回退
		return "toml"
	default:
		return "yaml"
	}
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
	// 其他类型（number, bool, nil）不可翻译，忽略
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
		// number, bool, nil — 原样返回
		return val
	}
}

// shortHash 生成字符串的短 hash（12 位 hex），用作 Segment ID。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}
