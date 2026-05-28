package prompt

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
)

//go:embed templates/bootstrap_system.tmpl
var defaultBootstrapTmpl string

// BootstrapData 是 bootstrap stage 渲染时的数据模型。
type BootstrapData struct {
	SourceLang string
	TargetLang string
	Texts      []string
	Existing   []string
	MaxTerms   int
}

// BootstrapRenderer 持有已编译的 bootstrap system 模板。user 由 Render 直接 JSON 序列化。
type BootstrapRenderer struct {
	system *template.Template
}

// NewBootstrapRenderer 加载内嵌模板。本次不支持自定义模板路径，避免膨胀配置面。
func NewBootstrapRenderer() (*BootstrapRenderer, error) {
	t, err := template.New("bootstrap_system").Parse(defaultBootstrapTmpl)
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

// Render 返回 (system, user, err)。user 永远是合法 JSON。
func (r *BootstrapRenderer) Render(d BootstrapData) (string, string, error) {
	if d.MaxTerms < 1 {
		d.MaxTerms = 20
	}
	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute bootstrap system: %w", err)
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
// 行为与 internal/pipeline/stages/translate.go 中的同名函数一致；为避免跨包依赖
// 在此独立维护一份（约 20 行）。
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
