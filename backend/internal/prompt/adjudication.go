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
	// Protocol 控制 system/user 协议与解析通道；由 ProtocolFromResponseMode 推导。
	Protocol Protocol
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

// Render 返回 (system, user, err)。ProtocolText 时 user 为纯文本格式，否则为 JSON。
func (r *AdjudicationRenderer) Render(d AdjudicationData) (string, string, error) {
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute adjudication system: %w", err)
	}
	if d.Protocol.IsText() {
		return sysBuf.String(), buildAdjudicationTextUser(d), nil
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

// buildAdjudicationTextUser 构建 text 模式的裁决 user message。
//
//	source_lang: ...
//	target_lang: ...
//
//	[segment] id=<id>
//	source: ...
//	target: ...
//	issues:
//	- code: message
func buildAdjudicationTextUser(d AdjudicationData) string {
	var sb strings.Builder
	sb.WriteString("source_lang: ")
	sb.WriteString(d.SourceLang)
	sb.WriteByte(10)
	sb.WriteString("target_lang: ")
	sb.WriteString(d.TargetLang)
	sb.WriteByte(10)
	for _, seg := range d.Segments {
		sb.WriteString("\n[segment] id=")
		sb.WriteString(seg.ID)
		sb.WriteByte(10)
		sb.WriteString("source: ")
		sb.WriteString(seg.Source)
		sb.WriteByte(10)
		sb.WriteString("target: ")
		sb.WriteString(seg.Target)
		sb.WriteByte(10)
		sb.WriteString("issues:\n")
		for _, iss := range seg.Issues {
			sb.WriteString("- ")
			sb.WriteString(iss.Code)
			sb.WriteString(": ")
			sb.WriteString(iss.Message)
			sb.WriteByte(10)
		}
	}
	return sb.String()
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

// ParseAdjudicationByMode 按 response mode 解析裁决响应。
// text 模式优先纯文本 [verdicts] 协议，空列表时 fallback JSON（模型常仍吐 JSON）。
func ParseAdjudicationByMode(text string, isTextMode bool) ([]AdjudicationVerdict, error) {
	if !isTextMode {
		return ParseAdjudicationResponse(text)
	}
	verdicts := parseAdjudicationTextVerdicts(text)
	if len(verdicts) > 0 {
		return verdicts, nil
	}
	return ParseAdjudicationResponse(text)
}

// parseAdjudicationTextVerdicts 解析 text 协议裁决输出：
//
//	[verdicts]
//	id | issue_code | verdict | reason
//
// reason 含 | 时取前三段为 id/issue_code/verdict，剩余并入 reason。
func parseAdjudicationTextVerdicts(text string) []AdjudicationVerdict {
	text = stripAdjudicationCodeFence(text)
	lines := strings.Split(text, "\n")
	inVerdicts := false
	hasHeader := false
	var out []AdjudicationVerdict

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[verdicts]") {
			inVerdicts = true
			hasHeader = true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if hasHeader {
				inVerdicts = false
			}
			continue
		}
		if hasHeader && !inVerdicts {
			continue
		}
		v := parseAdjudicationVerdictLine(line)
		if v == nil {
			continue
		}
		out = append(out, *v)
	}
	return out
}

func parseAdjudicationVerdictLine(line string) *AdjudicationVerdict {
	parts := strings.Split(line, "|")
	if len(parts) < 3 {
		return nil
	}
	id := strings.TrimSpace(parts[0])
	issueCode := strings.TrimSpace(parts[1])
	verdict := strings.TrimSpace(parts[2])
	reason := ""
	if len(parts) > 3 {
		rest := make([]string, 0, len(parts)-3)
		for _, p := range parts[3:] {
			rest = append(rest, strings.TrimSpace(p))
		}
		reason = strings.Join(rest, " | ")
	}
	if id == "" || issueCode == "" {
		return nil
	}
	if verdict != "real" && verdict != "false_positive" {
		return nil
	}
	return &AdjudicationVerdict{
		ID:        id,
		IssueCode: issueCode,
		Verdict:   verdict,
		Reason:    reason,
	}
}

// stripAdjudicationCodeFence 剥离 ```...``` 围栏（与 repair.stripCodeFence 行为一致，避免跨包依赖）。
func stripAdjudicationCodeFence(text string) string {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "```")
	if start < 0 {
		return text
	}
	afterStart := text[start+3:]
	if idx := strings.IndexByte(afterStart, '\n'); idx >= 0 {
		afterStart = afterStart[idx+1:]
	} else {
		return text
	}
	end := strings.LastIndex(afterStart, "```")
	if end < 0 {
		return strings.TrimSpace(afterStart)
	}
	return strings.TrimSpace(afterStart[:end])
}
