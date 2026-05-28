// Package jsonp 实现 JSON / YAML / TOML 结构化格式的解析与渲染。
//
// 解析策略
//
//	将文档解析为 map[string]any / []any 通用树，递归收集所有字符串叶子节点
//	作为可翻译的 Segment。路径信息（如 "config.title" 或 "items[0].name"）
//	保存在 Segment.Meta["path"] 中，Render 时通过路径将译文回写到树中。
//	原始树结构存储在 doc.Vars["_tree"] 中以供 Render 使用。
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
		Vars: map[string]any{
			"_tree": root,
		},
	}, nil
}

// Render 将翻译后的 Segment 回写到树结构并序列化为原始格式。
func (*Parser) Render(_ context.Context, doc *pipeline.Document, w io.Writer) error {
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

	// 从 Vars 取回原始树
	rawTree, ok := doc.Vars["_tree"]
	if !ok {
		return fmt.Errorf("jsonp: missing _tree in document vars")
	}

	// 构建翻译后的新树（不修改原始树）
	translated := buildTranslatedTree(rawTree, "", lookup)

	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()

	switch doc.Format {
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
	default:
		return fmt.Errorf("jsonp: unsupported format %q for render", doc.Format)
	}

	return bw.Flush()
}

// init 注册 jsonp parser。
func init() {
	parser.Register("structured", New())
}
