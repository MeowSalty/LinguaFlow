// Package prompt 提供基于 text/template 的 system 提示词渲染，
// 以及 JSON / text 形式的 user 消息构造。
//
// JSON 协议：user message 是 JSON envelope
//
//	{"source_lang":"...","target_lang":"...",
//	 "segments":{"<id>":{"source":"...","translate":true/false}, ...}}
//
// Text 协议：user message 是纯文本编号格式
//
//	[1] 需要翻译的段落
//	[*] 上下文参考段落
//
// 模型回复要求：
//   - JSON: {"translations":{"<id>":"<text>", ...}}
//   - Text: [编号] 翻译文本
package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// SingleID 是单段模式下 envelope 内唯一段的 id。translate stage 用它回写。
const SingleID = "0"

// RubyMode 的合法取值定义在 config 包中（RubyModeJSON / RubyModeInline / RubyModeSection）。

// RubyAnnotation 用于在提示词中展示 Ruby 标注信息。
type RubyAnnotation struct {
	Base string `json:"base"` // 基底文本
	Text string `json:"text"` // 标注文本
}

// GlossaryEntry 用于在提示词中展示术语命中。
// 故意独立于 glossary.Entry，避免循环依赖。
type GlossaryEntry struct {
	Source, Target, Notes string
}

// TMHint 用于在提示词中展示翻译记忆命中。
type TMHint struct {
	Source, Target string
	Score          float32
}

// SegmentInput 是批量翻译时的一段输入。
// ID 在 envelope 中作为 segments 的 key，需要在批内唯一且稳定；
// 单段模式下由 Render 自动用 SingleID 包装。
// Translate 默认 true；false 表示仅作上下文参考，不需要翻译。
type SegmentInput struct {
	ID        string
	Source    string
	Translate bool
}

// SegmentDetail 是 user message JSON 中每个 segment 的结构。
type SegmentDetail struct {
	Source    string `json:"source"`
	Translate bool   `json:"translate"`
}

// Data 是渲染时的数据模型。
// 单段模式：Source 非空，Segments 为空；Render 内部归一化为 [{ID:SingleID, Source:Source}]。
// 批量模式：Segments 非空，Source 为空。
type Data struct {
	SourceLang string
	TargetLang string
	Source     string
	Segments   []SegmentInput
	Glossary   []GlossaryEntry
	TMHints    []TMHint
	Vars       map[string]any

	InlineBootstrap   bool // 是否在 system prompt 中追加 inline 抽取指令（mode=inline 时由 translate stage 设为 true）
	MaxBootstrapTerms int  // inline 模式每批返回上限；仅在 InlineBootstrap=true 时有效
	StrictSchema      bool // 当后端使用 json_schema 强制输出时为 true；模板据此精简协议描述以节省 token
	TextMode          bool // 纯文本模式：user message 使用纯文本编号格式而非 JSON envelope

	RubyAnnotations map[string][]RubyAnnotation // segment ID → 标注列表
	RubyMode        string                      // "json" | "section" | ""
}

// HasRuby 判断当前数据中是否存在 Ruby 标注信息。
func (d Data) HasRuby() bool {
	for _, anns := range d.RubyAnnotations {
		if len(anns) > 0 {
			return true
		}
	}
	return false
}

// Renderer 持有已编译的 system 模板。
// user 消息由 Render 根据 TextMode 标志直接构建，无需模板。
type Renderer struct {
	system *template.Template
}

// templateFuncs 是模板函数映射。
var templateFuncs = template.FuncMap{
	"mul": func(a float32, b int) float64 {
		return float64(a) * float64(b)
	},
}

// NewRenderer 按配置创建 Renderer。
// 优先级：SystemTemplateContent（内联内容）> SystemTemplate（文件路径）。
// 缺少配置时直接报错，不再使用内置默认值。
func NewRenderer(cfg config.PromptConfig) (*Renderer, error) {
	if cfg.SystemTemplateContent == "" && cfg.SystemTemplate == "" {
		return nil, fmt.Errorf("prompt: system_template_content and system_template are both empty; configure a prompt template in your config file")
	}
	sys := cfg.SystemTemplateContent
	if cfg.SystemTemplate != "" {
		b, err := os.ReadFile(cfg.SystemTemplate)
		if err != nil {
			return nil, fmt.Errorf("prompt: read system template: %w", err)
		}
		sys = string(b)
	}
	systemT, err := template.New("system").Funcs(templateFuncs).Parse(sys)
	if err != nil {
		return nil, fmt.Errorf("prompt: parse system template: %w", err)
	}

	return &Renderer{system: systemT}, nil
}

// userEnvelope 是 JSON 模式 user message 的结构。
type userEnvelope struct {
	SourceLang      string                      `json:"source_lang,omitempty"`
	TargetLang      string                      `json:"target_lang,omitempty"`
	Segments        map[string]SegmentDetail    `json:"segments"`
	RubyAnnotations map[string][]RubyAnnotation `json:"ruby_annotations,omitempty"`
}

// Render 返回 (system, user, err)。TextMode 时 user 为纯文本编号格式，否则为 JSON。
func (r *Renderer) Render(d Data) (string, string, error) {
	segs := d.Segments
	if len(segs) == 0 {
		segs = []SegmentInput{{ID: SingleID, Source: d.Source, Translate: true}}
	}

	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute system: %w", err)
	}

	sys := sysBuf.String()
	if d.TextMode {
		mode := d.RubyMode
		if mode == "" {
			mode = config.RubyModeSection
		}
		return sys, buildTextUser(segs, d.RubyAnnotations, mode), nil
	}

	return sys, buildJSONUser(d, segs), nil
}

// buildJSONUser 构建 JSON 模式的 user message。
func buildJSONUser(d Data, segs []SegmentInput) string {
	segMap := make(map[string]SegmentDetail, len(segs))
	for _, s := range segs {
		segMap[s.ID] = SegmentDetail{
			Source:    s.Source,
			Translate: s.Translate,
		}
	}

	env := userEnvelope{
		SourceLang:      d.SourceLang,
		TargetLang:      d.TargetLang,
		Segments:        segMap,
		RubyAnnotations: d.RubyAnnotations,
	}
	userBytes, err := json.Marshal(env)
	if err != nil {
		// marshal 纯数据结构不应失败，防御性处理
		return "{}"
	}
	return string(userBytes)
}

// buildTextUser 构建 text 模式的 user message。
// 格式固定，与 parseBatchResponseLenientText 解析逻辑对应：
//   - 需要翻译的段落：[编号] 原文
//   - 上下文参考段落：[*] 原文
//   - rubyInputMode="inline"：注音以 ⟦ruby:base/text⟧ 内联到原文
//   - rubyInputMode="section"：注音以 [ruby] 独立段落追加
func buildTextUser(segs []SegmentInput, rubyAnnotations map[string][]RubyAnnotation, rubyInputMode string) string {
	var sb strings.Builder
	for i, s := range segs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if s.Translate {
			sb.WriteString("[")
			sb.WriteString(s.ID)
			sb.WriteString("] ")
			if rubyInputMode == config.RubyModeInline && len(rubyAnnotations) > 0 {
				sb.WriteString(inlineRubyInSource(s.Source, rubyAnnotations[s.ID]))
			} else {
				sb.WriteString(s.Source)
			}
		} else {
			sb.WriteString("[*] ")
			sb.WriteString(s.Source)
		}
	}

	if rubyInputMode == config.RubyModeSection && len(rubyAnnotations) > 0 {
		sb.WriteString("\n[ruby]")
		for _, s := range segs {
			if !s.Translate {
				continue
			}
			anns, ok := rubyAnnotations[s.ID]
			if !ok || len(anns) == 0 {
				continue
			}
			sb.WriteString("\n")
			sb.WriteString(s.ID)
			sb.WriteString(": ")
			for j, a := range anns {
				if j > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(a.Base)
				sb.WriteString("/")
				sb.WriteString(a.Text)
			}
		}
	}

	return sb.String()
}

// inlineRubyInSource 将注音以 ⟦ruby:base/text⟧ 格式内联到源文本中。
// 按注音顺序从左到右匹配基底文本，替换为标记。
func inlineRubyInSource(source string, anns []RubyAnnotation) string {
	if len(anns) == 0 {
		return source
	}
	var sb strings.Builder
	pos := 0
	for _, a := range anns {
		idx := strings.Index(source[pos:], a.Base)
		if idx == -1 {
			continue
		}
		sb.WriteString(source[pos : pos+idx])
		sb.WriteString("⟦ruby:")
		sb.WriteString(a.Base)
		sb.WriteString("/")
		sb.WriteString(a.Text)
		sb.WriteString("⟧")
		pos += idx + len(a.Base)
	}
	sb.WriteString(source[pos:])
	return sb.String()
}
