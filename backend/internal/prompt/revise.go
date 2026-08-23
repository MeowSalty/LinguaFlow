package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// ReviseIssue 是修订输入中的单条语义问题。
type ReviseIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Snippet 是触发问题的源/目标文本片段，用于让修订范围尽量精确。
	Snippet string `json:"snippet,omitempty"`
}

// ReviseSegment 是 LLM 修订输入中的单个段落。
type ReviseSegment struct {
	ID     string        `json:"id"`
	Source string        `json:"source"`
	Target string        `json:"target"`
	Issues []ReviseIssue `json:"issues"`
}

// ReviseData 是修订 prompt 渲染数据模型。
type ReviseData struct {
	SourceLang string
	TargetLang string
	Segments   []ReviseSegment
	// Protocol 控制 system/user 协议与解析通道；由 ProtocolFromResponseMode 推导。
	Protocol Protocol
}

// ReviseRenderer 持有已编译的修订 system 模板。user 由 Render 直接序列化。
type ReviseRenderer struct {
	system *template.Template
}

// NewReviseRenderer 按传入的模板内容创建 ReviseRenderer。
// 调用方负责注入模板内容（通常来自 templates.EmbeddedReviseTemplate）。
func NewReviseRenderer(systemContent string) (*ReviseRenderer, error) {
	if systemContent == "" {
		return nil, fmt.Errorf("prompt: revise system template content is empty")
	}
	t, err := template.New("revise_system").Parse(systemContent)
	if err != nil {
		return nil, fmt.Errorf("prompt: parse revise template: %w", err)
	}
	return &ReviseRenderer{system: t}, nil
}

// reviseEnvelope 是 user message 的 JSON 结构。
type reviseEnvelope struct {
	Task       string          `json:"task"`
	SourceLang string          `json:"source_lang,omitempty"`
	TargetLang string          `json:"target_lang,omitempty"`
	Segments   []ReviseSegment `json:"segments"`
}

// Render 返回 (system, user, err)。ProtocolText 时 user 为纯文本格式，否则为 JSON。
func (r *ReviseRenderer) Render(d ReviseData) (string, string, error) {
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute revise system: %w", err)
	}
	if d.Protocol.IsText() {
		return sysBuf.String(), buildReviseTextUser(d), nil
	}
	env := reviseEnvelope{
		Task:       "revise_translation",
		SourceLang: d.SourceLang,
		TargetLang: d.TargetLang,
		Segments:   d.Segments,
	}
	userBytes, err := json.Marshal(env)
	if err != nil {
		return "", "", fmt.Errorf("prompt: marshal revise envelope: %w", err)
	}
	return sysBuf.String(), string(userBytes), nil
}

// buildReviseTextUser 构建 text 模式的修订 user message。
//
//	source_lang: ...
//	target_lang: ...
//
//	[segment] id=<id>
//	source: ...
//	target: ...
//	issues:
//	- code | snippet: message
func buildReviseTextUser(d ReviseData) string {
	var sb strings.Builder
	sb.WriteString("source_lang: ")
	sb.WriteString(d.SourceLang)
	sb.WriteByte('\n')
	sb.WriteString("target_lang: ")
	sb.WriteString(d.TargetLang)
	sb.WriteByte('\n')
	for _, seg := range d.Segments {
		sb.WriteString("\n[segment] id=")
		sb.WriteString(seg.ID)
		sb.WriteByte('\n')
		sb.WriteString("source: ")
		sb.WriteString(seg.Source)
		sb.WriteByte('\n')
		sb.WriteString("target: ")
		sb.WriteString(seg.Target)
		sb.WriteByte('\n')
		sb.WriteString("issues:\n")
		for _, iss := range seg.Issues {
			sb.WriteString("- ")
			sb.WriteString(iss.Code)
			sb.WriteString(" | ")
			sb.WriteString(iss.Message)
			if iss.Snippet != "" {
				sb.WriteString(" | snippet: ")
				sb.WriteString(iss.Snippet)
			}
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// ReviseRevision 是 LLM 对单段的修订结果（整段修订后译文）。
type ReviseRevision struct {
	ID     string `json:"id"`
	Target string `json:"target"`
}

// ReviseRevisionSchema 返回 OpenAI 严格 JSON schema：
//
//	{revisions:[{id,target}]}
func ReviseRevisionSchema() map[string]any {
	itemProps := map[string]any{
		"id":     map[string]any{"type": "string"},
		"target": map[string]any{"type": "string"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"revisions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"properties":           itemProps,
					"required":             []string{"id", "target"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"revisions"},
		"additionalProperties": false,
	}
}

// NormalizeReviseRevisions 对解析出的 revisions 做 trim 与结构过滤：丢弃缺
// id/target 的条目。修订层不认识请求批次，越界 id 由调用方处理。
func NormalizeReviseRevisions(revisions []ReviseRevision) []ReviseRevision {
	out := revisions[:0]
	seen := make(map[string]struct{}, len(revisions))
	for _, rev := range revisions {
		rev.ID = strings.TrimSpace(rev.ID)
		rev.Target = strings.TrimSpace(rev.Target)
		if rev.ID == "" || rev.Target == "" {
			continue
		}
		// 同一 id 的重复结果保留首次出现，避免后续结果无依据地覆盖前一条。
		if _, dup := seen[rev.ID]; dup {
			continue
		}
		seen[rev.ID] = struct{}{}
		out = append(out, rev)
	}
	return out
}

// ParseReviseTextRevisions 解析 text 协议修订输出：
//
//	[revisions]
//	id | target
//
// target 含 | 时只按第一个分隔符切分，后续内容均属于 target。
// 返回 (revisions, recognized)：recognized=true 表示命中 [revisions] 协议（含空列表），
// 调用方据此决定是否 fallback JSON。
func ParseReviseTextRevisions(text string) ([]ReviseRevision, bool) {
	text = StripCodeFence(text)
	lines := strings.Split(text, "\n")
	inRevisions := false
	hasHeader := false
	hasRevisionContent := false
	var out []ReviseRevision

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[revisions]") {
			inRevisions = true
			hasHeader = true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if hasHeader {
				inRevisions = false
			}
			continue
		}
		if !hasHeader || !inRevisions {
			continue
		}
		hasRevisionContent = true
		rev := parseReviseRevisionLine(line)
		if rev == nil {
			continue
		}
		out = append(out, *rev)
	}
	return NormalizeReviseRevisions(out), hasHeader && (len(out) > 0 || !hasRevisionContent)
}

func parseReviseRevisionLine(line string) *ReviseRevision {
	sep := strings.IndexByte(line, '|')
	if sep < 0 {
		return nil
	}
	id := strings.TrimSpace(line[:sep])
	target := strings.TrimSpace(line[sep+1:])
	if id == "" || target == "" {
		return nil
	}
	return &ReviseRevision{ID: id, Target: target}
}
