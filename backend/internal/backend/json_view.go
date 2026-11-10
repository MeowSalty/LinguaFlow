package backend

import (
	"reflect"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// CheckJSONDepth checks complete or truncated text without allocating an arena.
// It also covers outer envelopes omitted by individual streaming prefix scans.
func CheckJSONDepth(s string) error {
	depth := 0
	quoted, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		if c == '{' || c == '[' {
			depth++
			if depth > MaxBuildDepth {
				return &BuildCapacityError{"JSON depth"}
			}
		} else if c == '}' || c == ']' {
			depth = max(0, depth-1)
		}
	}
	return nil
}

// JSONView preserves duplicate object fields with zero-copy byte slices.
// Construction validates depth/count and credits the complete offset arena.
type JSONView struct {
	b     *BuildBounds
	data  []byte
	nodes []jsonNode
}
type JSONValue struct {
	view  *JSONView
	index int
}

func NewJSONView(b *BuildBounds, data []byte) (JSONValue, error) {
	n, e := scanJSON(data, nil, b.nodeLimit)
	if e != nil {
		return JSONValue{}, e
	}
	if e = b.ReserveNodes(int64(n), int64(unsafe.Sizeof(jsonNode{}))); e != nil {
		return JSONValue{}, e
	}
	if e = b.ReserveNodes(1, 128); e != nil {
		return JSONValue{}, e
	}
	nodes := make([]jsonNode, n)
	if _, e = scanJSON(data, nodes, b.nodeLimit); e != nil {
		return JSONValue{}, e
	}
	view := &JSONView{b, data, nodes}
	return JSONValue{view, 0}, nil
}
func (v JSONValue) Kind() byte {
	if v.view == nil {
		return 0
	}
	return v.view.nodes[v.index].kind
}
func (v JSONValue) Raw() []byte { n := v.view.nodes[v.index]; return v.view.data[n.start:n.end] }
func (v JSONValue) Count() int  { return v.view.nodes[v.index].count }
func (v JSONValue) EachArray(fn func(JSONValue) error) error {
	n := v.view.nodes[v.index]
	for c := n.child; c >= 0; c = v.view.nodes[c].next {
		if e := fn(JSONValue{v.view, c}); e != nil {
			return e
		}
	}
	return nil
}
func (v JSONValue) EachField(fn func(JSONValue, JSONValue) error) error {
	n := v.view.nodes[v.index]
	for c := n.child; c >= 0; {
		value := v.view.nodes[c].next
		if e := fn(JSONValue{v.view, c}, JSONValue{v.view, value}); e != nil {
			return e
		}
		c = v.view.nodes[value].next
	}
	return nil
}
func (v JSONValue) String() (string, error) { return v.view.b.stringValue(v.Raw()) }

// StringEquals compares a validated JSON token without materializing a string.
// Field lookup can therefore scan repeated or large unknown keys at zero copy.
func (v JSONValue) StringEquals(want string) bool {
	if v.Kind() != '"' {
		return false
	}
	raw := v.Raw()
	at := 0
	for i := 1; i < len(raw)-1; i++ {
		var r rune
		if raw[i] != '\\' {
			r, n := utf8.DecodeRune(raw[i : len(raw)-1])
			i += n - 1
			if at >= len(want) {
				return false
			}
			w, nw := utf8.DecodeRuneInString(want[at:])
			if w != r {
				return false
			}
			at += nw
			continue
		}
		i++
		switch raw[i] {
		case '"', '\\', '/':
			r = rune(raw[i])
		case 'b':
			r = '\b'
		case 'f':
			r = '\f'
		case 'n':
			r = '\n'
		case 'r':
			r = '\r'
		case 't':
			r = '\t'
		case 'u':
			r = hexRune(raw[i+1 : i+5])
			i += 4
			if utf16.IsSurrogate(r) {
				if r >= 0xD800 && r <= 0xDBFF && i+6 < len(raw)-1 && raw[i+1] == '\\' && raw[i+2] == 'u' {
					r2 := hexRune(raw[i+3 : i+7])
					if r2 >= 0xDC00 && r2 <= 0xDFFF {
						r = utf16.DecodeRune(r, r2)
						i += 6
					} else {
						r = utf8.RuneError
					}
				} else {
					r = utf8.RuneError
				}
			}
		}
		if at >= len(want) {
			return false
		}
		w, n := utf8.DecodeRuneInString(want[at:])
		if w != r {
			return false
		}
		at += n
	}
	return at == len(want)
}
func (v JSONValue) StringLimited(maxBytes int64) (string, error) {
	if v.Kind() != '"' {
		return "", &BuildCapacityError{"JSON string kind"}
	}
	b := v.view.b
	old := b.stringLimit
	b.stringLimit = maxBytes
	defer func() { b.stringLimit = old }()
	return b.stringValue(v.Raw())
}
func (v JSONValue) DecodeInto(dst any, maxStringBytes int64) error {
	d := reflect.ValueOf(dst)
	if !d.IsValid() || d.Kind() != reflect.Pointer || d.IsNil() {
		return &BuildCapacityError{"JSON view destination"}
	}
	b := v.view.b
	old := b.stringLimit
	b.stringLimit = maxStringBytes
	defer func() { b.stringLimit = old }()
	return b.decodeValue(v.view.data, v.view.nodes, v.index, d.Elem())
}
func FirstJSONRaw(b *BuildBounds, data []byte) ([]byte, int, error) {
	s := jsonScan{data: data, limit: b.nodeLimit, maxDepth: MaxBuildDepth}
	s.space()
	start := s.pos
	if _, e := s.value(); e != nil {
		return nil, s.pos, e
	}
	return data[start:s.pos], s.pos, nil
}
