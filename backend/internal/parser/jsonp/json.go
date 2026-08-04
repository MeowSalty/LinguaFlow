// Package jsonp 实现 JSON / YAML / TOML 结构化格式的解析与渲染。
//
// 位置替换策略
//
//	Parse 时递归收集所有字符串叶子节点作为 Segment，路径信息保存在 Meta["path"]。
//	Render 时从原始文件重新解析为树，按 path 替换字符串值，再重新序列化。
//	这样可以避免 Parse 时序列化信息丢失（如 YAML 注释），也不再需要 Vars["_tree"]。
//
// 格式选择
//
//	Parse 优先信任调用方传入的 format 提示（扩展名来源，去点小写）；
//	仅当 format 为空时回退 detectFormat 内容探测。
//	Render 使用 doc.Format；为空时同样回退 detectFormat。
//
// 已知限制
//
//   - YAML / TOML 注释无法保留（通用 interface{} 反序列化）。
//   - 键序 round-trip 后可能变化（map 序列化由库决定）。
//   - TOML 日期时间类型（time.Time）为非字符串叶子，不进入翻译段，渲染时原样保留。
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
	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// Parser 可解析 .json / .yaml / .yml / .toml 文件。
type Parser struct{}

// New 构造一个 Parser。
func New() *Parser { return &Parser{} }

// Extensions 返回支持的结构化格式扩展名。
func (*Parser) Extensions() []string { return []string{".json", ".yaml", ".yml", ".toml"} }

// Parse 读取输入，按 format 提示（或内容探测）解析为通用树并收集字符串叶子节点。
func (*Parser) Parse(_ context.Context, r io.Reader, format string) (*pipeline.Document, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("jsonp: read input: %w", err)
	}
	content := strings.TrimSpace(string(data))
	fmtName := normalizeFormat(format)
	if content == "" {
		if fmtName == "" {
			fmtName = "json"
		}
		return &pipeline.Document{Format: fmtName}, nil
	}

	if fmtName == "" {
		fmtName = detectFormat(content)
	}

	var root any
	switch fmtName {
	case "json":
		if err := json.Unmarshal([]byte(content), &root); err != nil {
			return nil, fmt.Errorf("jsonp: json parse: %w", err)
		}
	case "yaml":
		if err := yaml.Unmarshal([]byte(content), &root); err != nil {
			return nil, fmt.Errorf("jsonp: yaml parse: %w", err)
		}
	case "toml":
		if err := toml.Unmarshal([]byte(content), &root); err != nil {
			return nil, fmt.Errorf("jsonp: toml parse: %w", err)
		}
	default:
		return nil, fmt.Errorf("jsonp: unknown format %q", fmtName)
	}

	var segments []pipeline.Segment
	collectStrings(root, "", &segments)

	return &pipeline.Document{
		Segments: segments,
		Format:   fmtName,
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

	format := normalizeFormat(strings.TrimSpace(doc.Format))
	if format == "" {
		format = detectFormat(content)
	}

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
	case "toml":
		if err := toml.Unmarshal([]byte(content), &root); err != nil {
			return fmt.Errorf("jsonp: toml parse original: %w", err)
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
	case "toml":
		enc := toml.NewEncoder(bw)
		if err := enc.Encode(translated); err != nil {
			return fmt.Errorf("jsonp: toml encode: %w", err)
		}
	}

	return bw.Flush()
}

// init 注册 jsonp parser。
func init() {
	parser.Register("structured", New())
}
