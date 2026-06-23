// Package prompt 提供基于 text/template 的 system 提示词渲染，
// 以及 JSON 形式的 user 消息构造。
//
// 协议：user message 是 JSON envelope
//
//	{"source_lang":"...","target_lang":"...","context_before":"...",
//	 "context_after":"...","segments":{"<id>":"<source>", ...}}
//
// 模型回复要求是 {"translations":{"<id>":"<text>", ...}}，由 translate stage 解析。
package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"text/template"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// SingleID 是单段模式下 envelope 内唯一段的 id。translate stage 用它回写。
const SingleID = "0"

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
type SegmentInput struct {
	ID     string
	Source string
}

// Data 是渲染时的数据模型。
// 单段模式：Source 非空，Segments 为空；Render 内部归一化为 [{ID:SingleID, Source:Source}]。
// 批量模式：Segments 非空，Source 为空。
type Data struct {
	SourceLang        string
	TargetLang        string
	Source            string
	Segments          []SegmentInput
	PrevContext       string
	NextContext       string
	Glossary          []GlossaryEntry
	TMHints           []TMHint
	Vars              map[string]any
	InlineBootstrap   bool // 是否在 system prompt 中追加 inline 抽取指令（mode=inline 时由 translate stage 设为 true）
	MaxBootstrapTerms int  // inline 模式每批返回上限；仅在 InlineBootstrap=true 时有效
	StrictSchema      bool // 当后端使用 json_schema 强制输出时为 true；模板据此精简协议描述以节省 token

	RubyAnnotations  map[string][]RubyAnnotation // segment ID → 标注列表
	RubyOutputFormat string                      // "ruby_output" | "inline_markers"
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

// Renderer 持有已编译的 system 模板。user 由 Render 直接 JSON 序列化生成，无模板。
type Renderer struct {
	system *template.Template
}

// NewRenderer 按配置创建 Renderer。
// 优先级：SystemTemplateContent（内联内容）> SystemTemplate（文件路径）。
// 缺少配置时直接报错，不再使用内置默认值。
// UserTemplate 字段保留以兼容旧 yaml，但当前协议下不再使用，非空时构造会失败提醒。
func NewRenderer(cfg config.PromptConfig) (*Renderer, error) {
	if cfg.UserTemplate != "" {
		return nil, fmt.Errorf("prompt: user_template is no longer supported (user message is built as JSON); remove it from config")
	}
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
	systemT, err := template.New("system").Parse(sys)
	if err != nil {
		return nil, fmt.Errorf("prompt: parse system template: %w", err)
	}
	return &Renderer{system: systemT}, nil
}

// userEnvelope 是 user message 的 JSON 结构。字段顺序仅为 encoding/json 的写出顺序，
// 模型读取无依赖。空字符串字段用 omitempty 省略以节省 token。
type userEnvelope struct {
	SourceLang      string                      `json:"source_lang,omitempty"`
	TargetLang      string                      `json:"target_lang,omitempty"`
	ContextBefore   string                      `json:"context_before,omitempty"`
	ContextAfter    string                      `json:"context_after,omitempty"`
	Segments        map[string]string           `json:"segments"`
	RubyAnnotations map[string][]RubyAnnotation `json:"ruby_annotations,omitempty"`
}

// Render 返回 (system, user, err)。user 永远是合法 JSON。
func (r *Renderer) Render(d Data) (string, string, error) {
	segs := d.Segments
	if len(segs) == 0 {
		segs = []SegmentInput{{ID: SingleID, Source: d.Source}}
	}
	segMap := make(map[string]string, len(segs))
	for _, s := range segs {
		segMap[s.ID] = s.Source
	}

	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute system: %w", err)
	}

	env := userEnvelope{
		SourceLang:      d.SourceLang,
		TargetLang:      d.TargetLang,
		ContextBefore:   d.PrevContext,
		ContextAfter:    d.NextContext,
		Segments:        segMap,
		RubyAnnotations: d.RubyAnnotations,
	}
	userBytes, err := json.Marshal(env)
	if err != nil {
		return "", "", fmt.Errorf("prompt: marshal user envelope: %w", err)
	}
	return sysBuf.String(), string(userBytes), nil
}
