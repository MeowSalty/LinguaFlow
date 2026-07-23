package prompt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
)

// AdjudicationIssue 是裁决输入中的单条规则问题。
type AdjudicationIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AdjudicationSegment 是裁决输入中的单个段落。
type AdjudicationSegment struct {
	ID     string              `json:"id"`
	Source string              `json:"source"`
	Target string              `json:"target"`
	Issues []AdjudicationIssue `json:"issues"`
}

// AdjudicationData 是裁决 prompt 渲染数据模型。
type AdjudicationData struct {
	SourceLang string
	TargetLang string
	Segments   []AdjudicationSegment
}

// AdjudicationRenderer 持有已编译的裁决 system 模板。user 由 Render 直接 JSON 序列化。
type AdjudicationRenderer struct {
	system *template.Template
}

// NewAdjudicationRenderer 按传入的模板内容创建 AdjudicationRenderer。
// 调用方负责注入模板内容（通常来自 templates.EmbeddedAdjudicationTemplate）。
func NewAdjudicationRenderer(systemContent string) (*AdjudicationRenderer, error) {
	if systemContent == "" {
		return nil, fmt.Errorf("prompt: adjudication system template content is empty")
	}
	t, err := template.New("adjudication_system").Parse(systemContent)
	if err != nil {
		return nil, fmt.Errorf("prompt: parse adjudication template: %w", err)
	}
	return &AdjudicationRenderer{system: t}, nil
}

// adjudicationEnvelope 是 user message 的 JSON 结构。
type adjudicationEnvelope struct {
	Task       string                `json:"task"`
	SourceLang string                `json:"source_lang,omitempty"`
	TargetLang string                `json:"target_lang,omitempty"`
	Segments   []AdjudicationSegment `json:"segments"`
}

// Render 返回 (system, user, err)。user 为 JSON envelope。
func (r *AdjudicationRenderer) Render(d AdjudicationData) (string, string, error) {
	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute adjudication system: %w", err)
	}
	env := adjudicationEnvelope{
		Task:       "adjudicate_quality_issues",
		SourceLang: d.SourceLang,
		TargetLang: d.TargetLang,
		Segments:   d.Segments,
	}
	userBytes, err := json.Marshal(env)
	if err != nil {
		return "", "", fmt.Errorf("prompt: marshal adjudication envelope: %w", err)
	}
	return sysBuf.String(), string(userBytes), nil
}

// AdjudicationVerdict 是 LLM 对单条 issue 的裁决结果。
type AdjudicationVerdict struct {
	ID        string `json:"id"`
	IssueCode string `json:"issue_code"`
	Verdict   string `json:"verdict"` // "real" | "false_positive"
	Reason    string `json:"reason"`
}

// AdjudicationVerdictSchema 返回 OpenAI 严格 JSON schema：
//
//	{verdicts:[{id,issue_code,verdict,reason}]}
func AdjudicationVerdictSchema() map[string]any {
	itemProps := map[string]any{
		"id": map[string]any{"type": "string"},
		"issue_code": map[string]any{
			"type": "string",
			"enum": []string{"source_residual", "length_ratio"},
		},
		"verdict": map[string]any{
			"type": "string",
			"enum": []string{"real", "false_positive"},
		},
		"reason": map[string]any{"type": "string"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdicts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"properties":           itemProps,
					"required":             []string{"id", "issue_code", "verdict", "reason"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"verdicts"},
		"additionalProperties": false,
	}
}

// ParseAdjudicationResponse 从 LLM 回复中提取首个 JSON 对象并解析 {verdicts:[...]}。
// 容错：允许 ```json 围栏与前后说明文字。
func ParseAdjudicationResponse(text string) ([]AdjudicationVerdict, error) {
	body := jsonObjectSlice(text)
	if body == "" {
		return nil, errors.New("no JSON object found in adjudication response")
	}
	var env struct {
		Verdicts []AdjudicationVerdict `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, fmt.Errorf("unmarshal verdicts: %w", err)
	}
	out := env.Verdicts[:0]
	for _, v := range env.Verdicts {
		v.ID = strings.TrimSpace(v.ID)
		v.IssueCode = strings.TrimSpace(v.IssueCode)
		v.Verdict = strings.TrimSpace(v.Verdict)
		v.Reason = strings.TrimSpace(v.Reason)
		if v.ID == "" || v.IssueCode == "" {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}
