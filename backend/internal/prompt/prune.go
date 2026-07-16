package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"
)

// PruneData 是 prune（术语精简）渲染时的数据模型。
type PruneData struct {
	SourceLang string
	TargetLang string
	Entries    []PruneEntry
}

// PruneEntry 是术语精简输入的单条术语，与 bootstrap 的 BootstrapEntry 结构相同。
type PruneEntry struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Notes  string `json:"notes"`
}

// PruneRenderer 持有已编译的 prune system 模板。user 由 Render 直接 JSON 序列化。
type PruneRenderer struct {
	system *template.Template
}

// NewPruneRenderer 按传入的模板内容创建 PruneRenderer。
// 调用方负责注入模板内容（通常来自 templates.EmbeddedPruneTemplate）。
func NewPruneRenderer(systemContent string) (*PruneRenderer, error) {
	if systemContent == "" {
		return nil, fmt.Errorf("prompt: prune system template content is empty")
	}
	t, err := template.New("prune_system").Parse(systemContent)
	if err != nil {
		return nil, fmt.Errorf("prompt: parse prune template: %w", err)
	}
	return &PruneRenderer{system: t}, nil
}

// pruneEnvelope 是 user message 的 JSON 结构。
type pruneEnvelope struct {
	Task       string       `json:"task"`
	SourceLang string       `json:"source_lang,omitempty"`
	TargetLang string       `json:"target_lang,omitempty"`
	Entries    []PruneEntry `json:"entries"`
}

// Render 返回 (system, user, err)。user 永远是合法 JSON。
func (r *PruneRenderer) Render(d PruneData) (string, string, error) {
	var sysBuf bytes.Buffer
	if err := r.system.Execute(&sysBuf, d); err != nil {
		return "", "", fmt.Errorf("prompt: execute prune system: %w", err)
	}
	env := pruneEnvelope{
		Task:       "refine_glossary",
		SourceLang: d.SourceLang,
		TargetLang: d.TargetLang,
		Entries:    d.Entries,
	}
	if env.Entries == nil {
		env.Entries = []PruneEntry{}
	}
	userBytes, err := json.Marshal(env)
	if err != nil {
		return "", "", fmt.Errorf("prompt: marshal prune envelope: %w", err)
	}
	return sysBuf.String(), string(userBytes), nil
}
