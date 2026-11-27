package prompt

import (
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type textCount struct{ n, limit, json int64 }

func (c *textCount) Write(p []byte) (int, error) {
	if int64(len(p)) > c.limit-c.n {
		return 0, &backend.BuildCapacityError{Resource: "prompt bytes"}
	}
	c.n += int64(len(p))
	c.json += backend.MeasureJSONStringBytes(p) - 2
	return len(p), nil
}
func (c *textCount) WriteString(s string) (int, error) {
	if int64(len(s)) > c.limit-c.n {
		return 0, &backend.BuildCapacityError{Resource: "prompt bytes"}
	}
	c.n += int64(len(s))
	n, e := backend.MeasureJSONString(s)
	if e != nil {
		return 0, e
	}
	c.json += n - 2
	return len(s), nil
}

type stringWriter interface {
	io.Writer
	io.StringWriter
}
type textOutput struct {
	w   stringWriter
	err error
}

func (o *textOutput) put(s string) {
	if o.err == nil {
		_, o.err = o.w.WriteString(s)
	}
}

func buildTextBounded(b *backend.BuildBounds, maxBytes int64, build func(*textOutput)) (string, error) {
	count := &textCount{limit: maxBytes}
	o := &textOutput{w: count}
	build(o)
	if o.err != nil {
		return "", o.err
	}
	w, e := backend.NewFixedBoundedWriter(b, count.n)
	if e != nil {
		return "", e
	}
	defer w.Release()
	o = &textOutput{w: &w}
	build(o)
	if o.err != nil {
		return "", o.err
	}
	if int64(len(w.Bytes())) != count.n {
		return "", fmt.Errorf("prompt changed during rendering")
	}
	if e = b.ReserveText(count.n, count.n); e != nil {
		return "", e
	}
	result := string(w.Bytes())
	w.Release()
	return result, nil
}

// Frozen data has no callbacks. Private copied Text backing is observable;
// the printer, parser and executor's internal temporaries are unknown.
func boundedTemplate(b *backend.BuildBounds, t *template.Template, maxBytes int64) (result *template.Template, retire func(), resultErr error) {
	var seen []string
	var parts []*template.Template
	var seenOwner, partsOwner *backend.OwnedAllocation
	var textOwners templateTextOwners
	defer func() {
		clear(seen)
		seen = nil
		clear(parts)
		parts = nil
		seenOwner.Release()
		partsOwner.Release()
		if resultErr != nil {
			result = nil
			textOwners.release()
		}
	}()
	addPart := func(part *template.Template) error {
		if len(part.Name()) > 128 || len(part.Tree.ParseName) > 128 {
			return &backend.BuildCapacityError{Resource: "template name metadata"}
		}
		if slices.Contains(seen, part.Name()) {
			return nil
		}
		if e := appendTemplateValue(b, &seen, &seenOwner, part.Name()); e != nil {
			return e
		}
		return appendTemplateValue(b, &parts, &partsOwner, part)
	}
	if e := addPart(t); e != nil {
		return nil, nil, e
	}
	syntaxNames := func(names []string, depth int) error {
		if len(names) > backend.MaxBuildDepth-depth {
			return &backend.BuildCapacityError{Resource: "template field path depth"}
		}
		for _, name := range names {
			if len(name) > 128 {
				return &backend.BuildCapacityError{Resource: "template identifier metadata"}
			}
		}
		return nil
	}
	var scan func(parse.Node, int) error
	scan = func(n parse.Node, depth int) error {
		if n == nil || reflect.ValueOf(n).IsNil() {
			return nil
		}
		if depth > backend.MaxBuildDepth {
			return &backend.BuildCapacityError{Resource: "template depth"}
		}
		switch x := n.(type) {
		case *parse.ListNode:
			for _, c := range x.Nodes {
				if e := scan(c, depth+1); e != nil {
					return e
				}
			}
		case *parse.RangeNode:
			if e := scan(x.Pipe, depth+1); e != nil {
				return e
			}
			if e := scan(x.List, depth+1); e != nil {
				return e
			}
			return scan(x.ElseList, depth+1)
		case *parse.IfNode:
			if e := scan(x.Pipe, depth+1); e != nil {
				return e
			}
			if e := scan(x.List, depth+1); e != nil {
				return e
			}
			return scan(x.ElseList, depth+1)
		case *parse.WithNode:
			if e := scan(x.Pipe, depth+1); e != nil {
				return e
			}
			if e := scan(x.List, depth+1); e != nil {
				return e
			}
			return scan(x.ElseList, depth+1)
		case *parse.ActionNode:
			return scan(x.Pipe, depth+1)
		case *parse.PipeNode:
			for _, d := range x.Decl {
				if e := syntaxNames(d.Ident, depth+1); e != nil {
					return e
				}
			}
			for _, c := range x.Cmds {
				if e := scan(c, depth+1); e != nil {
					return e
				}
			}
		case *parse.CommandNode:
			for _, a := range x.Args {
				if e := scan(a, depth+1); e != nil {
					return e
				}
			}
		case *parse.IdentifierNode:
			if len(x.Ident) > 128 {
				return &backend.BuildCapacityError{Resource: "template function metadata"}
			}
			if x.Ident == "call" {
				return fmt.Errorf("prompt callable template values are unsupported")
			}
		case *parse.TemplateNode:
			part := t.Lookup(x.Name)
			if part == nil {
				return fmt.Errorf("prompt nested template is missing")
			}
			if e := addPart(part); e != nil {
				return e
			}
			if e := scan(x.Pipe, depth+1); e != nil {
				return e
			}
			return scan(part.Tree.Root, depth+1)
		case *parse.TextNode:
			if int64(len(x.Text)) > maxBytes {
				return &backend.BuildCapacityError{Resource: "template literal bytes"}
			}
		case *parse.StringNode:
			if int64(len(x.Text)) > maxBytes {
				return &backend.BuildCapacityError{Resource: "template string literal bytes"}
			}
		case *parse.FieldNode:
			return syntaxNames(x.Ident, depth)
		case *parse.VariableNode:
			return syntaxNames(x.Ident, depth)
		case *parse.ChainNode:
			if e := syntaxNames(x.Field, depth); e != nil {
				return e
			}
			return scan(x.Node, depth+1)
		}
		return nil
	}
	if e := scan(t.Tree.Root, 0); e != nil {
		return nil, nil, e
	}
	clone := template.New(t.Name()).Funcs(templateFuncs)
	if t.Name() == "bounded" {
		clone.Option("missingkey=error")
	}
	var e error
	accountOutput := func(s string) (string, error) {
		if int64(len(s)) > maxBytes {
			return "", &backend.BuildCapacityError{Resource: "template output bytes"}
		}
		// The template executor can retain intermediate values in variables
		// and nested scopes. That private lifetime is unavailable here and
		// does not enter the allocation gate. Our final writer is explicit.
		return s, nil
	}
	emit := func(v any) (string, error) {
		rv := reflect.ValueOf(v)
		if rv.IsValid() && rv.Kind() == reflect.String {
			s := rv.String()
			if int64(len(s)) > maxBytes {
				return "", &backend.BuildCapacityError{Resource: "template action bytes"}
			}
			return s, nil // The executor borrows this existing string.
		}
		return accountOutput(fmt.Sprint(v))
	}
	format := func(format string, args ...any) (string, error) {
		return accountOutput(fmt.Sprintf(format, args...))
	}
	sprint := func(newline bool, args ...any) (string, error) {
		if newline {
			return accountOutput(fmt.Sprintln(args...))
		}
		return accountOutput(fmt.Sprint(args...))
	}
	escape := func(kind int, args ...any) (string, error) {
		s, err := sprint(false, args...)
		if err != nil {
			return "", err
		}
		var escaped string
		switch kind {
		case 0:
			escaped = template.HTMLEscapeString(s)
		case 1:
			escaped = template.JSEscapeString(s)
		default:
			escaped = template.URLQueryEscaper(s)
		}
		return accountOutput(escaped)
	}
	clone.Funcs(template.FuncMap{"eq": templateEqualBounded, "ne": templateNotEqualBounded, "__lf_emit": emit, "__lf_range": func(v any) (any, error) {
		rv := reflect.ValueOf(v)
		for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
			if rv.IsNil() {
				return v, nil
			}
			rv = rv.Elem()
		}
		switch rv.Kind() {
		case reflect.Invalid:
			return v, nil
		case reflect.Map, reflect.Array, reflect.Slice:
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			if rv.Uint() > math.MaxInt64 {
				return nil, &backend.BuildCapacityError{Resource: "template range records"}
			}
		default:
			return nil, errTemplateComparison // Avoid range's full-value error.
		}
		return v, nil
	}, "__lf_tick": func() (string, error) { return "", b.CheckContext() }, "printf": format, "print": func(args ...any) (string, error) { return sprint(false, args...) }, "println": func(args ...any) (string, error) { return sprint(true, args...) }, "html": func(args ...any) (string, error) { return escape(0, args...) }, "js": func(args ...any) (string, error) { return escape(1, args...) }, "urlquery": func(args ...any) (string, error) { return escape(2, args...) }})
	appendCommand := func(p *parse.PipeNode, name string) {
		p.Cmds = append(p.Cmds, &parse.CommandNode{NodeType: parse.NodeCommand, Args: []parse.Node{parse.NewIdentifier(name)}})
	}
	var rewrite func(parse.Node)
	rewrite = func(n parse.Node) {
		if n == nil || reflect.ValueOf(n).IsNil() {
			return
		}
		switch x := n.(type) {
		case *parse.ListNode:
			for _, c := range x.Nodes {
				rewrite(c)
			}
		case *parse.ActionNode:
			rewrite(x.Pipe)
			if len(x.Pipe.Decl) == 0 {
				appendCommand(x.Pipe, "__lf_emit")
			}
		case *parse.RangeNode:
			rewrite(x.Pipe)
			appendCommand(x.Pipe, "__lf_range")
			tick := &parse.ActionNode{NodeType: parse.NodeAction, Pipe: &parse.PipeNode{NodeType: parse.NodePipe, Cmds: []*parse.CommandNode{{NodeType: parse.NodeCommand, Args: []parse.Node{parse.NewIdentifier("__lf_tick")}}}}}
			x.List.Nodes = append([]parse.Node{tick}, x.List.Nodes...)
			rewrite(x.List)
			rewrite(x.ElseList)
		case *parse.IfNode:
			rewrite(x.Pipe)
			rewrite(x.List)
			rewrite(x.ElseList)
		case *parse.WithNode:
			rewrite(x.Pipe)
			rewrite(x.List)
			rewrite(x.ElseList)
		case *parse.TemplateNode:
			rewrite(x.Pipe)
		case *parse.PipeNode:
			for _, command := range x.Cmds {
				rewrite(command)
			}
		case *parse.CommandNode:
			for _, arg := range x.Args {
				rewrite(arg)
			}
		case *parse.ChainNode:
			rewrite(x.Node)
		case *parse.StringNode:
			x.Quoted = `"bounded literal"` // Text retains the actual value.
		case *parse.NumberNode:
			if len(x.Text) > 128 {
				// idealConstant uses syntactic flavor to select int vs float.
				// Preserve that flavor while retaining all parsed values.
				switch {
				case x.Text[0] == '\'':
					x.Text = "'x'"
				case len(x.Text) > 2 && x.Text[0] == '0' && (x.Text[1] == 'x' || x.Text[1] == 'X') && !strings.ContainsAny(x.Text, "pP"):
					x.Text = "0x0"
				case strings.ContainsAny(x.Text, ".eEpP"):
					x.Text = "0.0"
				default:
					x.Text = "0"
				}
			}
		}
	}
	for _, part := range parts {
		tree := part.Tree.Copy()
		if e = textOwners.hold(b, tree.Root); e != nil {
			tree = nil
			clone = nil
			return nil, nil, e
		}
		rewrite(tree.Root)
		if _, e = clone.AddParseTree(part.Name(), tree); e != nil {
			tree = nil
			clone = nil
			return nil, nil, e
		}
	}
	return clone, textOwners.release, nil
}

func renderSystemBounded(b *backend.BuildBounds, t *template.Template, d any, maxBytes int64) (string, error) {
	if e := validateTemplateData(b, reflect.ValueOf(d), 0); e != nil {
		return "", e
	}
	frozen, e := b.Clone(d)
	if e != nil {
		return "", e
	}
	return buildTextBounded(b, maxBytes, func(o *textOutput) {
		bt, retire, e := boundedTemplate(b, t, maxBytes)
		if e != nil {
			o.err = e
			return
		}
		o.err = bt.Execute(o.w, frozen)
		bt = nil
		retire()
	})
}

func validateTemplateData(b *backend.BuildBounds, v reflect.Value, depth int) error {
	if depth > backend.MaxBuildDepth {
		return &backend.BuildCapacityError{Resource: "template value depth"}
	}
	if !v.IsValid() {
		return nil
	}
	t := v.Type()
	if len(t.Name()) > 128 || len(t.String()) > 256 {
		return &backend.BuildCapacityError{Resource: "template type name"}
	}
	if t.NumMethod() > 0 && !(t.PkgPath() == "github.com/MeowSalty/LinguaFlow/backend/internal/prompt" && (t.Name() == "Data" || t.Name() == "ReviseData" || t.Name() == "Protocol")) {
		return fmt.Errorf("prompt callable value methods are unsupported")
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return validateTemplateData(b, v.Elem(), depth+1)
		}
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return fmt.Errorf("prompt callable values are unsupported")
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			if len(field.Name) > 128 || len(field.Tag) > 128 {
				return &backend.BuildCapacityError{Resource: "template field metadata"}
			}
			if e := validateTemplateData(b, v.Field(i), depth+1); e != nil {
				return e
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if e := validateTemplateData(b, v.Index(i), depth+1); e != nil {
				return e
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if e := validateTemplateData(b, iter.Value(), depth+1); e != nil {
				return e
			}
		}
	}
	return nil
}

// RenderTemplateBounded is the saved retry-template entry. Observable copied
// text is held through each execution; functions remain the fixed pure set.
func RenderTemplateBounded(b *backend.BuildBounds, content string, data any, maxBytes int64) (string, error) {
	if int64(len(content)) > maxBytes {
		return "", &backend.BuildCapacityError{Resource: "template source bytes"}
	}
	t, e := parseTemplateBounded(b, "bounded", content, true)
	if e != nil {
		return "", e
	}
	t.Option("missingkey=error")
	return renderSystemBounded(b, t, data, maxBytes)
}
func encodeUserBounded(b *backend.BuildBounds, v any, maxBytes int64) (string, error) {
	d, e := b.EncodeJSON(v, maxBytes)
	if e != nil {
		return "", e
	}
	if e = b.ReserveText(int64(len(d)), int64(len(d))); e != nil {
		return "", e
	}
	return string(d), nil
}

func (r *Renderer) RenderBounded(b *backend.BuildBounds, d Data, maxBytes int64) (string, string, error) {
	return r.RenderWithCreditsBounded(b, d, maxBytes, nil)
}
func (r *Renderer) RenderWithCreditsBounded(b *backend.BuildBounds, d Data, maxBytes int64, claim func(backend.PromptMeasure) error) (string, string, error) {
	return r.RenderWithSystemSuffixBounded(b, d, "", maxBytes, claim)
}
func (r *Renderer) RenderWithSystemSuffixBounded(b *backend.BuildBounds, d Data, suffix string, maxBytes int64, claim func(backend.PromptMeasure) error) (string, string, error) {
	if e := b.CloneInto(d, &d); e != nil {
		return "", "", e
	}
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	segs := d.Segments
	if len(segs) == 0 {
		if e := backend.ReserveSlice[SegmentInput](b, 1); e != nil {
			return "", "", e
		}
		segs = []SegmentInput{{ID: SingleID, Source: d.Source, Translate: true}}
	}
	if d.Protocol.IsText() {
		mode := d.RubyMode
		if mode == "" {
			mode = RubyModeSection
		}
		return renderPairWithSuffixBounded(b, r.system, d, suffix, maxBytes, claim, func(o *textOutput) { writeTranslateText(o, segs, d.RubyAnnotations, mode) })
	} else {
		m := make(map[string]SegmentDetail, len(segs))
		for _, s := range segs {
			m[s.ID] = SegmentDetail{Source: s.Source, Translate: s.Translate}
		}
		env := userEnvelope{d.SourceLang, d.TargetLang, m, d.RubyAnnotations}
		return renderPairWithSuffixBounded(b, r.system, d, suffix, maxBytes, claim, func(o *textOutput) { o.err = b.WriteJSON(o.w, env) })
	}
}

func writeAnnotations(o *textOutput, anns []RubyAnnotation) {
	for i, a := range anns {
		if i > 0 {
			o.put(", ")
		}
		o.put(a.Base)
		o.put("/")
		o.put(a.Text)
		if a.ID != "" {
			o.put("#")
			o.put(a.ID)
		}
	}
}
func writeTranslateText(o *textOutput, segs []SegmentInput, annotations map[string][]RubyAnnotation, mode string) {
	for i, s := range segs {
		if i > 0 {
			o.put("\n")
		}
		if !s.Translate {
			o.put("[*] ")
			o.put(s.Source)
			continue
		}
		o.put("[")
		o.put(s.ID)
		o.put("] ")
		if mode != RubyModeInline {
			o.put(s.Source)
			continue
		}
		pos := 0
		for _, a := range annotations[s.ID] {
			j := strings.Index(s.Source[pos:], a.Base)
			if j < 0 {
				continue
			}
			o.put(s.Source[pos : pos+j])
			o.put("⟦ruby:")
			o.put(a.Base)
			o.put("/")
			o.put(a.Text)
			o.put("⟧")
			pos += j + len(a.Base)
		}
		o.put(s.Source[pos:])
	}
	if mode == RubyModeSection && len(annotations) > 0 {
		o.put("\n[ruby]")
		for _, s := range segs {
			if !s.Translate || len(annotations[s.ID]) == 0 {
				continue
			}
			o.put("\n")
			o.put(s.ID)
			o.put(": ")
			writeAnnotations(o, annotations[s.ID])
		}
	}
}

func (r *ReviseRenderer) RenderBounded(b *backend.BuildBounds, d ReviseData, maxBytes int64) (string, string, error) {
	return r.RenderWithCreditsBounded(b, d, maxBytes, nil)
}
func (r *ReviseRenderer) RenderWithCreditsBounded(b *backend.BuildBounds, d ReviseData, maxBytes int64, claim func(backend.PromptMeasure) error) (string, string, error) {
	if e := b.CloneInto(d, &d); e != nil {
		return "", "", e
	}
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	if !d.Protocol.IsText() {
		env := reviseEnvelope{"revise_translation", d.SourceLang, d.TargetLang, d.Segments, d.RubyAnnotations}
		return renderPairBounded(b, r.system, d, maxBytes, claim, func(o *textOutput) { o.err = b.WriteJSON(o.w, env) })
	} else {
		return renderPairBounded(b, r.system, d, maxBytes, claim, func(o *textOutput) {
			o.put("source_lang: ")
			o.put(d.SourceLang)
			o.put("\ntarget_lang: ")
			o.put(d.TargetLang)
			o.put("\n")
			for _, seg := range d.Segments {
				o.put("\n[segment] id=")
				o.put(seg.ID)
				o.put("\nsource: ")
				o.put(seg.Source)
				o.put("\ntarget: ")
				o.put(seg.Target)
				o.put("\n")
				if a := d.RubyAnnotations[seg.ID]; len(a) > 0 {
					o.put("ruby: ")
					writeAnnotations(o, a)
					o.put("\n")
				}
				o.put("issues:\n")
				for _, v := range seg.Issues {
					o.put("- ")
					o.put(v.Code)
					o.put(" | ")
					o.put(v.Message)
					if v.Snippet != "" {
						o.put(" | snippet: ")
						o.put(v.Snippet)
					}
					o.put("\n")
				}
			}
		})
	}
}
