package prompt

import (
	"fmt"
	"strings"
	"text/template"
	"unicode"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func parseTemplateBounded(b *backend.BuildBounds, name, content string, funcs bool) (*template.Template, error) {
	if content == "" {
		return nil, fmt.Errorf("prompt template content is empty")
	}
	count := int64(1)
	depth, sp := 0, 0
	var parents [64]int
	for pos := 0; pos < len(content); {
		at := strings.Index(content[pos:], "{{")
		if at < 0 {
			count++
			break
		}
		pos += at + 2
		count++
		start := pos
		quote := byte(0)
		escaped := false
		parens := 0
		for pos < len(content) {
			c := content[pos]
			count++
			if count > 16380 {
				return nil, &backend.BuildCapacityError{Resource: "template syntax nodes"}
			}
			if quote != 0 {
				if escaped {
					escaped = false
				} else if c == '\\' && quote != '`' {
					escaped = true
				} else if c == quote {
					quote = 0
				}
				pos++
				continue
			}
			if c == '"' || c == '\'' || c == '`' {
				quote = c
				pos++
				continue
			}
			if c == '(' {
				parens++
				if parens > 64 {
					return nil, &backend.BuildCapacityError{Resource: "template expression depth"}
				}
			}
			if c == ')' {
				parens = max(0, parens-1)
			}
			if c == '}' && pos+1 < len(content) && content[pos+1] == '}' {
				break
			}
			pos++
		}
		action := strings.TrimSpace(content[start:pos])
		action = strings.TrimSpace(strings.TrimPrefix(action, "-"))
		word := action
		if at := strings.IndexFunc(action, unicode.IsSpace); at >= 0 {
			word = action[:at]
		}
		switch word {
		case "if", "range", "with", "define", "block":
			if sp == 64 {
				return nil, &backend.BuildCapacityError{Resource: "template syntax depth"}
			}
			parents[sp] = depth
			sp++
			depth++
		case "else":
			tail := strings.TrimSpace(strings.TrimPrefix(action, "else"))
			if strings.HasPrefix(tail, "if ") || strings.HasPrefix(tail, "with ") {
				depth++
			}
		case "end":
			if sp > 0 {
				sp--
				depth = parents[sp]
			}
		}
		if depth > 64 {
			return nil, &backend.BuildCapacityError{Resource: "template syntax depth"}
		}
		pos = min(len(content), pos+2)
	}
	if e := b.ReserveText(0, 4*int64(len(content))); e != nil {
		return nil, e
	}
	if e := b.ReserveNodes(4*count+16, 1024); e != nil {
		return nil, e
	}
	t := template.New(name)
	if funcs {
		t.Funcs(templateFuncs)
	}
	t, e := t.Parse(content)
	if e != nil {
		return nil, fmt.Errorf("invalid frozen prompt template")
	}
	return t, nil
}
func NewRendererBounded(b *backend.BuildBounds, s string) (*Renderer, error) {
	t, e := parseTemplateBounded(b, "system", s, true)
	if e != nil {
		return nil, e
	}
	return &Renderer{system: t}, nil
}
func NewBootstrapRendererBounded(b *backend.BuildBounds, s string) (*BootstrapRenderer, error) {
	t, e := parseTemplateBounded(b, "bootstrap_system", s, false)
	if e != nil {
		return nil, e
	}
	return &BootstrapRenderer{system: t}, nil
}
func NewPruneRendererBounded(b *backend.BuildBounds, s string) (*PruneRenderer, error) {
	t, e := parseTemplateBounded(b, "prune_system", s, false)
	if e != nil {
		return nil, e
	}
	return &PruneRenderer{system: t}, nil
}
func NewSemanticQARendererBounded(b *backend.BuildBounds, s string) (*SemanticQARenderer, error) {
	t, e := parseTemplateBounded(b, "semantic_qa_system", s, false)
	if e != nil {
		return nil, e
	}
	return &SemanticQARenderer{system: t}, nil
}
func NewAdjudicationRendererBounded(b *backend.BuildBounds, s string) (*AdjudicationRenderer, error) {
	t, e := parseTemplateBounded(b, "adjudication_system", s, false)
	if e != nil {
		return nil, e
	}
	return &AdjudicationRenderer{system: t}, nil
}
func NewReviseRendererBounded(b *backend.BuildBounds, s string) (*ReviseRenderer, error) {
	t, e := parseTemplateBounded(b, "revise_system", s, false)
	if e != nil {
		return nil, e
	}
	return &ReviseRenderer{system: t}, nil
}
