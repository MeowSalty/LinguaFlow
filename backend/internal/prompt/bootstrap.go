package prompt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
)

// BootstrapData 是 bootstrap stage 渲染时的数据模型。
type BootstrapData struct {
	SourceLang   string
	TargetLang   string
	Texts        []string
	Existing     []string
	MaxTerms     int
	TextMode     bool // 纯文本模式：user message 使用纯文本格式而非 JSON envelope
	StrictSchema bool // 后端以 json_schema 强制结构时为 true；模板可省略完整 JSON 形状示例
}

// BootstrapRenderer 持有已编译的 bootstrap system 模板。user 由 Render 直接 JSON 序列化。
type BootstrapRenderer struct {
	system *template.Template
}

// NewBootstrapRenderer 按传入的模板内容创建 BootstrapRenderer。
// 调用方负责注入模板内容（通常来自 templates.EmbeddedBootstrapTemplate）。
func NewBootstrapRenderer(systemContent string) (*BootstrapRenderer, error) {
	if systemContent == "" {
		return nil, fmt.Errorf("prompt: bootstrap system template content is empty")
	}
	t, err := template.New("bootstrap_system").Parse(systemContent)
	if err != nil {
		return nil, fmt.Errorf("prompt: parse bootstrap template: %w", err)
	}
	return &BootstrapRenderer{system: t}, nil
}

// bootstrapEnvelope 是 user message 的 JSON 结构。
type bootstrapEnvelope struct {
	Task       string   `json:"task"`
	SourceLang string   `json:"source_lang,omitempty"`
	TargetLang string   `json:"target_lang,omitempty"`
	Existing   []string `json:"existing,omitempty"`
	Texts      []string `json:"texts"`
}

// Render 返回 (system, user, err)。TextMode 时 user 为纯文本格式，否则为 JSON。
func (r *BootstrapRenderer) Render(d BootstrapData) (string, string, error) {
	if d.MaxTerms < 1 {
		d.MaxTerms = 20
	}
	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute bootstrap system: %w", err)
	}
	if d.TextMode {
		return sysBuf.String(), buildBootstrapTextUser(d), nil
	}
	env := bootstrapEnvelope{
		Task:       "extract_terms",
		SourceLang: d.SourceLang,
		TargetLang: d.TargetLang,
		Existing:   d.Existing,
		Texts:      d.Texts,
	}
	userBytes, err := json.Marshal(env)
	if err != nil {
		return "", "", fmt.Errorf("prompt: marshal bootstrap envelope: %w", err)
	}
	return sysBuf.String(), string(userBytes), nil
}

// buildBootstrapTextUser 构建 text 模式的 bootstrap user message。
//
//	source_lang: ...
//	target_lang: ...
//	max_terms: N
//
//	[existing]
//	term1
//	...
//
//	[texts]
//	---
//	<text0>
//	---
//	<text1>
func buildBootstrapTextUser(d BootstrapData) string {
	var sb strings.Builder
	sb.WriteString("source_lang: ")
	sb.WriteString(d.SourceLang)
	sb.WriteByte('\n')
	sb.WriteString("target_lang: ")
	sb.WriteString(d.TargetLang)
	sb.WriteByte('\n')
	sb.WriteString("max_terms: ")
	sb.WriteString(fmt.Sprintf("%d", d.MaxTerms))
	sb.WriteByte('\n')

	if len(d.Existing) > 0 {
		sb.WriteString("\n[existing]\n")
		for _, e := range d.Existing {
			sb.WriteString(e)
			sb.WriteByte('\n')
		}
	}

	sb.WriteString("\n[texts]\n")
	for _, t := range d.Texts {
		sb.WriteString("---\n")
		sb.WriteString(t)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// BootstrapEntry 是 LLM 抽取出的一条候选术语。与 glossary.Entry 解耦，避免循环依赖。
type BootstrapEntry struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Notes  string `json:"notes"`
}

// BootstrapSchema 返回 OpenAI 严格 JSON schema：
//
//	{glossary:[{source:string,target:string,notes:string}]}
//
// 严格模式要求每个对象列出 required = 所有属性、additionalProperties=false。
func BootstrapSchema() map[string]any {
	itemProps := map[string]any{
		"source": map[string]any{"type": "string"},
		"target": map[string]any{"type": "string"},
		"notes":  map[string]any{"type": "string"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"glossary": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"properties":           itemProps,
					"required":             []string{"source", "target", "notes"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"glossary"},
		"additionalProperties": false,
	}
}

// ParseBootstrapResponse 从 LLM 回复中提取首个 JSON 对象并解析 {glossary:[...]}。
// 容错：允许 ```json 围栏与前后说明文字。
func ParseBootstrapResponse(text string) ([]BootstrapEntry, error) {
	body := jsonObjectSlice(text)
	if body == "" {
		return nil, errors.New("no JSON object found in bootstrap response")
	}
	var env struct {
		Glossary []BootstrapEntry `json:"glossary"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, fmt.Errorf("unmarshal glossary: %w", err)
	}
	out := env.Glossary[:0]
	seen := make(map[string]struct{}, len(env.Glossary))
	for _, e := range env.Glossary {
		e.Source = strings.TrimSpace(e.Source)
		e.Target = strings.TrimSpace(e.Target)
		e.Notes = strings.TrimSpace(e.Notes)
		if e.Source == "" || e.Target == "" {
			continue
		}
		// LLM 偶尔重复同一 source；保留首次出现。
		if _, dup := seen[e.Source]; dup {
			continue
		}
		seen[e.Source] = struct{}{}
		out = append(out, e)
	}
	return out, nil
}

// jsonObjectSlice 从 text 中截取首个 { 到与之配对的 } 之间的子串。
// 为避免跨包依赖在此独立维护一份（约 20 行）。
func jsonObjectSlice(text string) string {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}
