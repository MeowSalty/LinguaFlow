package prompt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
)

// SemanticQASegment 是语义质检输入中的单个段落。
type SemanticQASegment struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
}

// SemanticQAData 是语义质检 prompt 渲染数据模型。
type SemanticQAData struct {
	SourceLang string
	TargetLang string
	Segments   []SemanticQASegment
	// Protocol 控制 system/user 协议与解析通道；由 ProtocolFromResponseMode 推导。
	Protocol Protocol
}

// SemanticQARenderer 持有已编译的语义质检 system 模板。user 由 Render 直接序列化。
type SemanticQARenderer struct {
	system *template.Template
}

// NewSemanticQARenderer 按传入的模板内容创建 SemanticQARenderer。
// 调用方负责注入模板内容（通常来自 templates.EmbeddedSemanticQATemplate）。
func NewSemanticQARenderer(systemContent string) (*SemanticQARenderer, error) {
	if systemContent == "" {
		return nil, fmt.Errorf("prompt: semantic_qa system template content is empty")
	}
	t, err := template.New("semantic_qa_system").Parse(systemContent)
	if err != nil {
		return nil, fmt.Errorf("prompt: parse semantic_qa template: %w", err)
	}
	return &SemanticQARenderer{system: t}, nil
}

// semanticQAEnvelope 是 user message 的 JSON 结构。
type semanticQAEnvelope struct {
	Task       string              `json:"task"`
	SourceLang string              `json:"source_lang,omitempty"`
	TargetLang string              `json:"target_lang,omitempty"`
	Segments   []SemanticQASegment `json:"segments"`
}

// Render 返回 (system, user, err)。ProtocolText 时 user 为纯文本格式，否则为 JSON。
func (r *SemanticQARenderer) Render(d SemanticQAData) (string, string, error) {
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute semantic_qa system: %w", err)
	}
	if d.Protocol.IsText() {
		return sysBuf.String(), buildSemanticQATextUser(d), nil
	}
	env := semanticQAEnvelope{
		Task:       "semantic_quality_scan",
		SourceLang: d.SourceLang,
		TargetLang: d.TargetLang,
		Segments:   d.Segments,
	}
	userBytes, err := json.Marshal(env)
	if err != nil {
		return "", "", fmt.Errorf("prompt: marshal semantic_qa envelope: %w", err)
	}
	return sysBuf.String(), string(userBytes), nil
}

// buildSemanticQATextUser 构建 text 模式的语义质检 user message。
//
//	source_lang: ...
//	target_lang: ...
//
//	[segment] id=<id>
//	source: ...
//	target: ...
func buildSemanticQATextUser(d SemanticQAData) string {
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
	}
	return sb.String()
}

// SemanticQAIssue 是 LLM 对单条语义问题的扫描结果。
type SemanticQAIssue struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// allowedSemanticQACodes 本次支持的语义 issue code。
var allowedSemanticQACodes = map[string]struct{}{
	"calque":        {},
	"term_fidelity": {},
	"naturalness":   {},
}

// IsSemanticQACode 报告 code 是否由语义质检轮次维护。
func IsSemanticQACode(code string) bool {
	_, ok := allowedSemanticQACodes[code]
	return ok
}

// SemanticQAIssueSchema 返回 OpenAI 严格 JSON schema：
//
//	{issues:[{id,code,message}]}
func SemanticQAIssueSchema() map[string]any {
	itemProps := map[string]any{
		"id": map[string]any{"type": "string"},
		"code": map[string]any{
			"type": "string",
			"enum": []string{"calque", "term_fidelity", "naturalness"},
		},
		"message": map[string]any{"type": "string"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"issues": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"properties":           itemProps,
					"required":             []string{"id", "code", "message"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"issues"},
		"additionalProperties": false,
	}
}

// ParseSemanticQAResponse 从 LLM 回复中提取首个 JSON 对象并解析 {issues:[...]}。
// 容错：允许 ```json 围栏与前后说明文字。
func ParseSemanticQAResponse(text string) ([]SemanticQAIssue, error) {
	body := jsonObjectSlice(text)
	if body == "" {
		return nil, errors.New("no JSON object found in semantic_qa response")
	}
	var env struct {
		Issues []SemanticQAIssue `json:"issues"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, fmt.Errorf("unmarshal issues: %w", err)
	}
	out := env.Issues[:0]
	for _, iss := range env.Issues {
		iss.ID = strings.TrimSpace(iss.ID)
		iss.Code = strings.TrimSpace(iss.Code)
		iss.Message = strings.TrimSpace(iss.Message)
		if iss.ID == "" || iss.Code == "" {
			continue
		}
		if !IsSemanticQACode(iss.Code) {
			continue
		}
		out = append(out, iss)
	}
	return out, nil
}

// ParseSemanticQAByMode 按 response mode 解析语义质检响应。
// text 模式优先纯文本 [issues] 协议，空列表时 fallback JSON（模型常仍吐 JSON）。
func ParseSemanticQAByMode(text string, isTextMode bool) ([]SemanticQAIssue, error) {
	if !isTextMode {
		return ParseSemanticQAResponse(text)
	}
	issues, recognized := parseSemanticQATextIssues(text)
	if recognized {
		return issues, nil
	}
	return ParseSemanticQAResponse(text)
}

// parseSemanticQATextIssues 解析 text 协议语义质检输出：
//
//	[issues]
//	id | code | message
//
// message 含 | 时取前两段为 id/code，剩余并入 message。
func parseSemanticQATextIssues(text string) ([]SemanticQAIssue, bool) {
	text = stripAdjudicationCodeFence(text)
	lines := strings.Split(text, "\n")
	inIssues := false
	hasHeader := false
	hasIssueContent := false
	var out []SemanticQAIssue

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[issues]") {
			inIssues = true
			hasHeader = true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if hasHeader {
				inIssues = false
			}
			continue
		}
		if hasHeader && !inIssues {
			continue
		}
		if hasHeader {
			hasIssueContent = true
		}
		iss := parseSemanticQAIssueLine(line)
		if iss == nil {
			continue
		}
		out = append(out, *iss)
	}
	return out, len(out) > 0 || (hasHeader && !hasIssueContent)
}

func parseSemanticQAIssueLine(line string) *SemanticQAIssue {
	parts := strings.Split(line, "|")
	if len(parts) < 2 {
		return nil
	}
	id := strings.TrimSpace(parts[0])
	code := strings.TrimSpace(parts[1])
	message := ""
	if len(parts) > 2 {
		rest := make([]string, 0, len(parts)-2)
		for _, p := range parts[2:] {
			rest = append(rest, strings.TrimSpace(p))
		}
		message = strings.Join(rest, " | ")
	}
	if id == "" || code == "" {
		return nil
	}
	if !IsSemanticQACode(code) {
		return nil
	}
	return &SemanticQAIssue{
		ID:      id,
		Code:    code,
		Message: message,
	}
}
