// Package jsonp 实现 JSON / YAML / TOML 结构化格式的解析与渲染。
//
// 位置替换策略
//
//	Parse 时递归收集所有字符串叶子节点作为 Segment，路径信息保存在 Meta["path"]。
//	Render 时从原始文件重新解析为树，按 path 替换字符串值，再重新序列化。
//	这样可以避免 Parse 时序列化信息丢失（如 YAML 注释），也不再需要 Vars["_tree"]。
//
// 已知限制
//
//   - YAML 注释无法保留（gopkg.in/yaml.v3 的 Node 类型可保留注释，
//     但当前使用通用 interface{} 反序列化）。
//   - TOML 格式暂未实现。
package jsonp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"gopkg.in/yaml.v3"
)

// Parser 可解析 .json / .yaml / .yml / .toml 文件。
type Parser struct{}

// New 构造一个 Parser。
func New() *Parser { return &Parser{} }

// Extensions 返回支持的结构化格式扩展名。
func (*Parser) Extensions() []string { return []string{".json", ".yaml", ".yml", ".toml"} }

// Parse 读取输入，检测格式，解析为通用树并收集所有字符串叶子节点作为 Segment。
func (*Parser) Parse(_ context.Context, r io.Reader) (*pipeline.Document, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("jsonp: read input: %w", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return &pipeline.Document{Format: "json"}, nil
	}

	format := detectFormat(content)
	var root any
	switch format {
	case "json":
		if err := json.Unmarshal([]byte(content), &root); err != nil {
			return nil, fmt.Errorf("jsonp: json parse: %w", err)
		}
	case "yaml":
		if err := yaml.Unmarshal([]byte(content), &root); err != nil {
			return nil, fmt.Errorf("jsonp: yaml parse: %w", err)
		}
	case "toml":
		return nil, fmt.Errorf("jsonp: toml not yet implemented")
	default:
		return nil, fmt.Errorf("jsonp: unknown format %q", format)
	}

	var segments []pipeline.Segment
	collectStrings(root, "", &segments)

	return &pipeline.Document{
		Segments: segments,
		Format:   format,
	}, nil
}

// Render 从 original 重新解析树结构，按 path 替换字符串值，再序列化输出。
func (*Parser) Render(_ context.Context, doc *pipeline.Document, original io.Reader, w io.Writer) error {
	// 构建路径 → 译文 的查找表
	lookup := make(map[string]string, len(doc.Segments))
	for _, seg := range doc.Segments {
		path, ok := seg.Meta["path"].(string)
		if !ok || path == "" {
			continue
		}
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		lookup[path] = target
	}

	// 从 original 读取并重新解析
	data, err := io.ReadAll(original)
	if err != nil {
		return fmt.Errorf("jsonp: read original: %w", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil
	}

	format := detectFormat(content)
	var root any
	switch format {
	case "json":
		if err := json.Unmarshal([]byte(content), &root); err != nil {
			return fmt.Errorf("jsonp: json parse original: %w", err)
		}
	case "yaml":
		if err := yaml.Unmarshal([]byte(content), &root); err != nil {
			return fmt.Errorf("jsonp: yaml parse original: %w", err)
		}
	default:
		return fmt.Errorf("jsonp: unsupported format %q for render", format)
	}

	// 构建翻译后的新树
	translated := buildTranslatedTree(root, "", lookup)

	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()

	switch format {
	case "json":
		enc := json.NewEncoder(bw)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(translated); err != nil {
			return fmt.Errorf("jsonp: json encode: %w", err)
		}
	case "yaml":
		enc := yaml.NewEncoder(bw)
		enc.SetIndent(2)
		if err := enc.Encode(translated); err != nil {
			return fmt.Errorf("jsonp: yaml encode: %w", err)
		}
	}

	return bw.Flush()
}

// init 注册 jsonp parser。
func init() {
	parser.Register("structured", New())
}
