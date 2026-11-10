package prompt

import (
	"fmt"
	"io"
	"reflect"
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
	o = &textOutput{w: w}
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
	return string(w.Bytes()), nil
}

// Template callbacks and formatting must be bounded before text/template's
// own fmt printer allocates. Trees are private per render; frozen data is never
// allowed to contain callable values. Every range receives credit before the
// standard executor creates its sorted map workspace.
func boundedTemplate(b *backend.BuildBounds, t *template.Template, maxBytes int64) (*template.Template, error) {
	if e := b.ReserveNodes(4, 1024); e != nil {
		return nil, e
	}
	seen := make(map[string]bool)
	var parts []*template.Template
	addPart := func(part *template.Template) error {
		if len(part.Name()) > 128 || len(part.Tree.ParseName) > 128 {
			return &backend.BuildCapacityError{Resource: "template name metadata"}
		}
		if seen[part.Name()] {
			return nil
		}
		if e := b.ReserveNodes(1, 1024); e != nil {
			return e
		}
		seen[part.Name()] = true
		var e error
		parts, e = backend.AppendNode(b, parts, part)
		return e
	}
	if e := addPart(t); e != nil {
		return nil, e
	}
	var count int64
	syntaxNames := func(names []string, depth int) error {
		if len(names) > backend.MaxBuildDepth-depth {
			return &backend.BuildCapacityError{Resource: "template field path depth"}
		}
		for _, name := range names {
			if len(name) > 128 {
				return &backend.BuildCapacityError{Resource: "template identifier metadata"}
			}
		}
		// Copied name headers and bounded diagnostic/context expansion.
		return b.ReserveNodes(int64(2*len(names)+1), 1024)
	}
	var scan func(parse.Node, int) error
	scan = func(n parse.Node, depth int) error {
		if n == nil || reflect.ValueOf(n).IsNil() {
			return nil
		}
		if depth > backend.MaxBuildDepth {
			return &backend.BuildCapacityError{Resource: "template depth"}
		}
		count++
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
			if e := b.ReserveText(int64(len(x.Text)), int64(len(x.Text))); e != nil {
				return e
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
		return nil, e
	}
	if e := b.ReserveNodes(4*count+16, 1024); e != nil {
		return nil, e
	}
	clone := template.New(t.Name()).Funcs(templateFuncs)
	if t.Name() == "bounded" {
		clone.Option("missingkey=error")
	}
	var e error
	emit := func(v any) (string, error) {
		rv := reflect.ValueOf(v)
		if rv.IsValid() && rv.Kind() == reflect.String {
			s := rv.String()
			if int64(len(s)) > maxBytes {
				return "", &backend.BuildCapacityError{Resource: "template action bytes"}
			}
			// The standard executor still uses fmt.Fprint for the returned
			// string. Precredit its append backing and the prior backing.
			if e := b.ReserveFormatWorkspace(); e != nil {
				return "", e
			}
			if e := b.ReserveText(int64(len(s)), 2*int64(len(s))); e != nil {
				return "", e
			}
			if e := b.ReserveText(0, int64(len(s))); e != nil {
				return "", e
			}
			return s, nil
		}
		n, _, e := measureFmt(b, rv, 0)
		if e != nil {
			return "", e
		}
		if n > maxBytes-128 {
			return "", &backend.BuildCapacityError{Resource: "template formatted bytes"}
		}
		if e = reserveFmtOutput(b, n+128); e != nil {
			return "", e
		}
		if e = b.ReserveNodes(2, 1024); e != nil {
			return "", e
		}
		return fmt.Sprint(v), nil
	}
	format := func(format string, args ...any) (string, error) {
		// The format itself is frozen, but arbitrary widths can request an
		// enormous fmt buffer. Check every numeric width/precision first.
		var largest int64
		var slots int64
		for _, a := range args {
			n, count, e := measureFmt(b, reflect.ValueOf(a), 0)
			if e != nil {
				return "", e
			}
			largest = max(largest, n)
			slots = max(slots, count)
		}
		upper := int64(len(format)) + 128
		argIndex := 0
		reordered := false
		if upper > maxBytes {
			return "", &backend.BuildCapacityError{Resource: "template format bytes"}
		}
		for i := 0; i < len(format); i++ {
			if format[i] != '%' {
				continue
			}
			i++
			if i < len(format) && format[i] == '%' {
				continue
			}
			width := int64(0)
			for ; i < len(format); i++ {
				c := format[i]
				if c >= '0' && c <= '9' {
					// A width and a precision are separate integer tokens.
					var digits int64
					for i < len(format) && format[i] >= '0' && format[i] <= '9' {
						if digits > (maxBytes-int64(format[i]-'0'))/10 {
							return "", &backend.BuildCapacityError{Resource: "template format width"}
						}
						digits = digits*10 + int64(format[i]-'0')
						i++
					}
					width = max(width, digits)
					i--
					continue
				}
				if c == '.' || c == '-' || c == '+' || c == '#' || c == ' ' || c == '0' {
					continue
				}
				if c == '[' {
					reordered = true
					// fmt's explicit index changes the next argument, including
					// width/precision stars. Invalid indices cannot allocate output.
					index, valid := 0, true
					i++
					start := i
					for i < len(format) && format[i] >= '0' && format[i] <= '9' {
						digit := int(format[i] - '0')
						if index > len(args) || index > (len(args)-digit)/10 {
							valid = false
						} else {
							index = index*10 + digit
						}
						i++
					}
					if i >= len(format) || format[i] != ']' || i == start || index < 1 || index > len(args) {
						valid = false
					}
					if valid {
						argIndex = index - 1
					} else {
						argIndex = len(args)
					}
					continue
				}
				if c == '*' {
					if argIndex < len(args) {
						rv := reflect.ValueOf(args[argIndex])
						if rv.IsValid() && rv.Kind() >= reflect.Int && rv.Kind() <= reflect.Int64 {
							v := rv.Int()
							if v < 0 {
								if v == -1<<63 {
									return "", &backend.BuildCapacityError{Resource: "template format width"}
								}
								v = -v
							}
							if v > maxBytes {
								return "", &backend.BuildCapacityError{Resource: "template format width"}
							}
							width = max(width, v)
						} else if rv.IsValid() && rv.Kind() >= reflect.Uint && rv.Kind() <= reflect.Uintptr {
							v := rv.Uint()
							if v > uint64(maxBytes) {
								return "", &backend.BuildCapacityError{Resource: "template format width"}
							}
							width = max(width, int64(v))
						}
					}
					if argIndex < len(args) {
						argIndex++
					}
					continue
				}
				if c != '%' && argIndex < len(args) {
					argIndex++
				}
				break
			}
			// fmt applies padding/precision to each member of a composite
			// value. Count all visited records, not just one directive.
			if largest > maxBytes-upper-128 || (slots > 0 && width > (maxBytes-upper-128-largest)/slots) {
				return "", &backend.BuildCapacityError{Resource: "template format bytes"}
			}
			add := largest + width*slots + 128
			if add > maxBytes-upper {
				return "", &backend.BuildCapacityError{Resource: "template format bytes"}
			}
			upper += add
		}
		// fmt appends every unused operand as an EXTRA diagnostic, unless
		// an explicit index disabled that check. Even an empty format can
		// therefore emit a complete large value.
		if !reordered {
			for _, a := range args[argIndex:] {
				n, _, e := measureFmt(b, reflect.ValueOf(a), 0)
				if e != nil {
					return "", e
				}
				if n > maxBytes-upper-258 {
					return "", &backend.BuildCapacityError{Resource: "template extra format arguments"}
				}
				upper += n + 258 // <=256-byte type spelling, '=' and separator.
			}
		}
		if e := reserveFmtOutput(b, upper); e != nil {
			return "", e
		}
		if e := b.ReserveNodes(2, 1024); e != nil {
			return "", e
		}
		return fmt.Sprintf(format, args...), nil
	}
	sprint := func(newline bool, args ...any) (string, error) {
		upper := int64(128 + len(args))
		for _, a := range args {
			n, _, e := measureFmt(b, reflect.ValueOf(a), 0)
			if e != nil {
				return "", e
			}
			if n > maxBytes-upper {
				return "", &backend.BuildCapacityError{Resource: "template print bytes"}
			}
			upper += n
		}
		if e := reserveFmtOutput(b, upper); e != nil {
			return "", e
		}
		if e := b.ReserveNodes(int64(len(args))+2, 1024); e != nil {
			return "", e
		}
		if newline {
			return fmt.Sprintln(args...), nil
		}
		return fmt.Sprint(args...), nil
	}
	escape := func(kind int, args ...any) (string, error) {
		s, e := sprint(false, args...)
		if e != nil {
			return "", e
		}
		factor := int64(6)
		if kind == 2 {
			factor = 3
		}
		if int64(len(s)) > maxBytes/factor {
			return "", &backend.BuildCapacityError{Resource: "template escaped bytes"}
		}
		if e = b.ReserveText(0, 3*factor*int64(len(s))); e != nil {
			return "", e
		}
		switch kind {
		case 0:
			return template.HTMLEscapeString(s), nil
		case 1:
			return template.JSEscapeString(s), nil
		default:
			return template.URLQueryEscaper(s), nil
		}
	}
	clone.Funcs(template.FuncMap{"eq": templateEqualBounded, "ne": templateNotEqualBounded, "__lf_emit": emit, "__lf_range": func(v any) (any, error) {
		rv := reflect.ValueOf(v)
		for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
			if rv.IsNil() {
				return v, nil
			}
			rv = rv.Elem()
		}
		var count int64
		switch rv.Kind() {
		case reflect.Invalid:
			return v, nil
		case reflect.Map, reflect.Array, reflect.Slice:
			count = int64(rv.Len())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			count = max(int64(0), rv.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			if rv.Uint() > 65536 {
				return nil, &backend.BuildCapacityError{Resource: "template range records"}
			}
			count = int64(rv.Uint())
		default:
			return nil, errTemplateComparison // Avoid range's full-value error.
		}
		if count >= 65536 {
			return nil, &backend.BuildCapacityError{Resource: "template range records"}
		}
		if e := b.ReserveNodes(4*count+1, 1024); e != nil {
			return nil, e
		}
		return v, nil
	}, "__lf_tick": func() (string, error) { return "", b.ReserveNodes(1, 256) }, "printf": format, "print": func(args ...any) (string, error) { return sprint(false, args...) }, "println": func(args ...any) (string, error) { return sprint(true, args...) }, "html": func(args ...any) (string, error) { return escape(0, args...) }, "js": func(args ...any) (string, error) { return escape(1, args...) }, "urlquery": func(args ...any) (string, error) { return escape(2, args...) }})
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
		rewrite(tree.Root)
		if _, e = clone.AddParseTree(part.Name(), tree); e != nil {
			return nil, e
		}
	}
	return clone, nil
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
		bt, e := boundedTemplate(b, t, maxBytes)
		if e != nil {
			o.err = e
			return
		}
		o.err = bt.Execute(o.w, frozen)
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
			if e := b.ReserveNodes(1, 256); e != nil {
				return e
			}
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
		if e := b.ReserveNodes(1, 128); e != nil {
			return e
		}
		iter := v.MapRange()
		for iter.Next() {
			if e := b.ReserveNodes(1, 1024); e != nil {
				return e
			}
			if e := validateTemplateData(b, iter.Value(), depth+1); e != nil {
				return e
			}
		}
	}
	return nil
}

// measureFmt bounds fmt's supported reflective value output independently of
// JSON tags/numeric encoding. Each record allows 512 ASCII bytes for its finite
// scalar representation, <=256-byte type spelling, field label and diagnostics;
// string bytes additionally allow six-byte escapes. float64 fixed decimal has
// at most 309 integer digits plus sign/default six fractional digits. Dynamic
// precision and width are added per record by the directive parser.
func reserveFmtOutput(b *backend.BuildBounds, upper int64) error {
	// Go 1.26 fmt may reuse a <=64KiB pooled buffer. Its writePadding
	// growth is 2*oldCap+n (<3*newLen); appends/quoted numeric scratch
	// need <=2*upper, while an old backing has <=upper before growth.
	// Reserve each possible backing separately, including allocator rounding.
	if e := b.ReserveFormatWorkspace(); e != nil {
		return e
	}
	for _, factor := range [...]int64{3, 2, 1} {
		if upper > (1<<63-1)/factor {
			return &backend.BuildCapacityError{Resource: "formatted backing bytes"}
		}
		if e := b.ReserveText(0, factor*upper); e != nil {
			return e
		}
	}
	return nil
}

func measureFmt(b *backend.BuildBounds, v reflect.Value, depth int) (int64, int64, error) {
	if depth > backend.MaxBuildDepth {
		return 0, 0, &backend.BuildCapacityError{Resource: "formatted value depth"}
	}
	if !v.IsValid() {
		return 512, 1, nil
	}
	if e := b.ReserveNodes(1, 1024); e != nil {
		return 0, 0, e
	}
	if v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64 {
		// internal/strconv.bigFtoa/roundShortest have at most three
		// 824-byte decimal records, regardless of precision or input size.
		if e := b.ReserveNodes(3, 1024); e != nil {
			return 0, 0, e
		}
	}
	if len(v.Type().String()) > 256 {
		return 0, 0, &backend.BuildCapacityError{Resource: "formatted type spelling"}
	}
	upper, slots := int64(512), int64(1)
	if v.Kind() == reflect.Complex64 || v.Kind() == reflect.Complex128 {
		// Complex constants can come from template syntax even though
		// frozen metadata disallows complex values. Both components use
		// the same finite floating formatter and receive their own padding.
		if e := b.ReserveNodes(6, 1024); e != nil {
			return 0, 0, e
		}
		upper, slots = 1024, 2
	}
	add := func(child reflect.Value) error {
		n, count, e := measureFmt(b, child, depth+1)
		if e != nil {
			return e
		}
		if n > (1<<63-1)-upper || count > (1<<63-1)-slots {
			return &backend.BuildCapacityError{Resource: "formatted value bytes"}
		}
		upper += n
		slots += count
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			if e := add(v.Elem()); e != nil {
				return 0, 0, e
			}
		}
	case reflect.String:
		n := int64(v.Len())
		if n > ((1<<63-1)-upper)/6 {
			return 0, 0, &backend.BuildCapacityError{Resource: "formatted string bytes"}
		}
		upper += 6 * n
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if e := add(v.Field(i)); e != nil {
				return 0, 0, e
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if e := add(v.Index(i)); e != nil {
				return 0, 0, e
			}
		}
	case reflect.Map:
		if e := b.ReserveNodes(int64(v.Len())*4+1, 1024); e != nil {
			return 0, 0, e
		}
		iter := v.MapRange()
		for iter.Next() {
			if e := b.ReserveNodes(1, 1024); e != nil {
				return 0, 0, e
			}
			if e := add(iter.Key()); e != nil {
				return 0, 0, e
			}
			if e := add(iter.Value()); e != nil {
				return 0, 0, e
			}
		}
	}
	return upper, slots, nil
}

// RenderTemplateBounded is the saved retry-template entry. Parsing its private
// tree is credited before allocation; functions remain the fixed pure set.
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
	if e := b.CloneInto(d, &d); e != nil {
		return "", "", e
	}
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	segs := d.Segments
	if len(segs) == 0 {
		if e := b.ReserveNodes(1, 64); e != nil {
			return "", "", e
		}
		segs = []SegmentInput{{ID: SingleID, Source: d.Source, Translate: true}}
	}
	if d.Protocol.IsText() {
		mode := d.RubyMode
		if mode == "" {
			mode = RubyModeSection
		}
		return renderPairBounded(b, r.system, d, maxBytes, claim, func(o *textOutput) { writeTranslateText(o, segs, d.RubyAnnotations, mode) })
	} else {
		if e := b.ReserveNodes(int64(len(segs))+1, 1024); e != nil {
			return "", "", e
		}
		m := make(map[string]SegmentDetail, len(segs))
		for _, s := range segs {
			m[s.ID] = SegmentDetail{Source: s.Source, Translate: s.Translate}
		}
		env := userEnvelope{d.SourceLang, d.TargetLang, m, d.RubyAnnotations}
		return renderPairBounded(b, r.system, d, maxBytes, claim, func(o *textOutput) { o.err = b.WriteJSON(o.w, env) })
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
