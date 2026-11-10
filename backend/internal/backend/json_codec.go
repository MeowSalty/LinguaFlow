package backend

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

var rawJSONType = reflect.TypeFor[json.RawMessage]()
var numberType = reflect.TypeFor[json.Number]()
var timeType = reflect.TypeFor[time.Time]()

type decoderField struct {
	name  string
	index int
}
type decoderFieldCache struct {
	types map[reflect.Type][]decoderField
}

func (b *BuildBounds) fieldsFor(t reflect.Type) ([]decoderField, error) {
	if b.fieldCache != nil {
		if fields, ok := b.fieldCache.types[t]; ok {
			return fields, nil
		}
	} else {
		if e := b.ReserveNodes(2, 1024); e != nil {
			return nil, e
		}
		b.fieldCache = &decoderFieldCache{types: make(map[reflect.Type][]decoderField)}
	}
	if e := b.ReserveNodes(int64(t.NumField()), int64(unsafe.Sizeof(decoderField{}))); e != nil {
		return nil, e
	}
	fields := make([]decoderField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if e := b.ReserveNodes(1, 512); e != nil {
			return nil, e
		}
		field := t.Field(i)
		if len(field.Name) > 128 || len(field.Tag) > 128 {
			return nil, &BuildCapacityError{"JSON field metadata"}
		}
		name, _, ok := jsonField(field)
		if ok {
			fields = append(fields, decoderField{name, i})
		}
	}
	if e := b.ReserveNodes(1, 1024); e != nil {
		return nil, e
	}
	b.fieldCache.types[t] = fields
	return fields, nil
}

// WireString emits concatenated string fragments without cloning a large prompt.
type WireString []string

var wireStringType = reflect.TypeFor[WireString]()

type jsonCounter struct{ bytes, limit int64 }

func (c *jsonCounter) WriteString(s string) (int, error) {
	if int64(len(s)) > c.limit-c.bytes {
		return 0, &BuildCapacityError{"JSON size"}
	}
	c.bytes += int64(len(s))
	return len(s), nil
}

func (c *jsonCounter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.limit-c.bytes {
		return 0, &BuildCapacityError{"JSON size"}
	}
	c.bytes += int64(len(p))
	return len(p), nil
}

func (b *BuildBounds) MeasureJSON(v any) (int64, error) {
	b.encodeNodes = 0
	// Counting borrowed text does not allocate it. The caller's exact J/I
	// grant bounds the later output; a standalone default must not silently
	// become a different maximum for a configured large request.
	c := &jsonCounter{limit: math.MaxInt64}
	err := b.encodeValue(c, reflect.ValueOf(v), false)
	return c.bytes, err
}

func (b *BuildBounds) WriteJSON(w io.Writer, v any) error {
	b.encodeNodes = 0
	return b.encodeValue(w, reflect.ValueOf(v), true)
}

func (b *BuildBounds) DecodeJSONLimited(data []byte, v any, maxStringBytes int64) error {
	if maxStringBytes < 0 {
		return &BuildCapacityError{"decoded string limit"}
	}
	old := b.stringLimit
	b.stringLimit = maxStringBytes
	defer func() { b.stringLimit = old }()
	return b.DecodeJSON(data, v)
}

func (b *BuildBounds) EncodeJSON(v any, maxBytes int64) ([]byte, error) {
	n, err := b.MeasureJSON(v)
	if err != nil {
		return nil, err
	}
	if n > maxBytes || n > int64(math.MaxInt) || n < 0 {
		return nil, &BuildCapacityError{"encoded JSON bytes"}
	}
	if err = b.ReserveText(n, n); err != nil {
		return nil, err
	}
	data := make([]byte, 0, int(n))
	w := &fixedJSONWriter{data: data}
	b.encodeNodes = 0
	if err = b.encodeValue(w, reflect.ValueOf(v), true); err != nil {
		return nil, err
	}
	if int64(len(w.data)) != n {
		return nil, errors.New("JSON changed between measurement and encoding")
	}
	return w.data, nil
}

type fixedJSONWriter struct{ data []byte }

func (w *fixedJSONWriter) WriteString(s string) (int, error) {
	if len(s) > cap(w.data)-len(w.data) {
		return 0, &BuildCapacityError{"JSON measurement drift"}
	}
	w.data = append(w.data, s...)
	return len(s), nil
}

func (w *fixedJSONWriter) Write(p []byte) (int, error) {
	if len(p) > cap(w.data)-len(w.data) {
		return 0, &BuildCapacityError{"JSON measurement drift"}
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func writeLiteral(w io.Writer, s string) error { _, err := io.WriteString(w, s); return err }
func writeJSONString(w io.Writer, s string) error {
	if err := writeLiteral(w, "\""); err != nil {
		return err
	}
	if err := writeJSONText(w, s); err != nil {
		return err
	}
	return writeLiteral(w, "\"")
}

func shortJSONEscape(c byte) byte {
	switch c {
	case '\b':
		return 'b'
	case '\f':
		return 'f'
	case '\n':
		return 'n'
	case '\r':
		return 'r'
	case '\t':
		return 't'
	default:
		return 0
	}
}

func writeJSONText(w io.Writer, s string) error {
	var scratch [6]byte
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' && c < utf8.RuneSelf {
			start := i
			for i < len(s) && s[i] >= 0x20 && s[i] != '"' && s[i] != '\\' && s[i] != '<' && s[i] != '>' && s[i] != '&' && s[i] < utf8.RuneSelf {
				i++
			}
			if err := writeLiteral(w, s[start:i]); err != nil {
				return err
			}
			continue
		}
		if c == '"' || c == '\\' {
			scratch[0] = '\\'
			scratch[1] = c
			if _, err := w.Write(scratch[:2]); err != nil {
				return err
			}
			i++
			continue
		}
		// Match encoding/json's canonical short escapes. Frozen Ruby digests
		// compare its historical Marshal bytes with this bounded encoder.
		if escaped := shortJSONEscape(c); escaped != 0 {
			scratch[0], scratch[1] = '\\', escaped
			if _, err := w.Write(scratch[:2]); err != nil {
				return err
			}
			i++
			continue
		}
		if c < 0x20 || c == '<' || c == '>' || c == '&' {
			const hex = "0123456789abcdef"
			scratch = [6]byte{'\\', 'u', '0', '0', hex[c>>4], hex[c&15]}
			if _, err := w.Write(scratch[:]); err != nil {
				return err
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == 0x2028 || r == 0x2029 {
			scratch = [6]byte{'\\', 'u', '2', '0', '2', byte('0' + r - 0x2020)}
			if _, err := w.Write(scratch[:]); err != nil {
				return err
			}
			i += n
			continue
		}
		if r == utf8.RuneError && n == 1 {
			if err := writeLiteral(w, "\\ufffd"); err != nil {
				return err
			}
			i++
			continue
		}
		if err := writeLiteral(w, s[i:i+n]); err != nil {
			return err
		}
		i += n
	}
	return nil
}

func jsonField(f reflect.StructField) (string, bool, bool) {
	if f.PkgPath != "" {
		return "", false, false
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, false
	}
	name, options, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	omit := false
	for options != "" {
		var p string
		p, options, _ = strings.Cut(options, ",")
		if p == "omitempty" || p == "omitzero" {
			omit = true
		}
	}
	return name, omit, true
}

func (b *BuildBounds) encodeValue(w io.Writer, v reflect.Value, materialize bool) error {
	if err := b.Enter(); err != nil {
		return err
	}
	defer b.Leave()
	if b.encodeNodes >= b.nodeLimit {
		return &BuildCapacityError{"encoded JSON nodes"}
	}
	b.encodeNodes++
	if !v.IsValid() {
		return writeLiteral(w, "null")
	}
	for hops := 0; v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer; hops++ {
		if hops >= MaxBuildDepth {
			return &BuildCapacityError{"pointer nesting"}
		}
		if v.IsNil() {
			return writeLiteral(w, "null")
		}
		v = v.Elem()
	}
	if v.Type() == measuredJSONStringType {
		if materialize {
			return &BuildCapacityError{"measured string cannot be encoded"}
		}
		c, ok := w.(*jsonCounter)
		if !ok {
			return &BuildCapacityError{"measured string destination"}
		}
		n := v.Field(0).Int()
		if n < 2 || n > c.limit-c.bytes {
			return &BuildCapacityError{"JSON size"}
		}
		c.bytes += n
		return nil
	}
	if v.Type() == rawJSONType {
		if v.IsNil() {
			return writeLiteral(w, "null")
		}
		d := v.Bytes()
		n, err := scanJSONBudget(d, nil, b.nodeLimit-b.encodeNodes+1, MaxBuildDepth-b.depth+1)
		if err != nil {
			return err
		}
		b.encodeNodes += int64(n) - 1
		_, err = w.Write(d)
		return err
	}
	if v.Type() == numberType {
		n := v.String()
		if len(n) == 0 || len(n) > 64 || n[0] != '-' && (n[0] < '0' || n[0] > '9') {
			return &BuildCapacityError{"JSON numeric token"}
		}
		if err := b.ReserveNodes(1, 64); err != nil {
			return err
		}
		var scratch [64]byte
		copy(scratch[:], n)
		if _, err := scanJSON(scratch[:len(n)], nil, 1); err != nil {
			return err
		}
		return writeLiteral(w, n)
	}
	if v.Type() == timeType {
		if err := b.ReserveNodes(1, 128); err != nil {
			return err
		}
		t := v.Interface().(time.Time)
		_, offset := t.Zone()
		if t.Year() < 0 || t.Year() > 9999 || offset <= -24*60*60 || offset >= 24*60*60 {
			return &BuildCapacityError{"RFC3339 time"}
		}
		var scratch [64]byte
		d := t.AppendFormat(scratch[:0], time.RFC3339Nano)
		if len(d) > 64 {
			return &BuildCapacityError{"RFC3339 time"}
		}
		if err := writeLiteral(w, "\""); err != nil {
			return err
		}
		if _, err := w.Write(d); err != nil {
			return err
		}
		return writeLiteral(w, "\"")
	}
	if v.Type() == wireStringType {
		if err := writeLiteral(w, "\""); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if err := b.ReserveNodes(1, 64); err != nil {
				return err
			}
			if err := writeJSONText(w, v.Index(i).String()); err != nil {
				return err
			}
		}
		return writeLiteral(w, "\"")
	}
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return writeLiteral(w, "true")
		}
		return writeLiteral(w, "false")
	case reflect.String:
		if err := b.ReserveNodes(1, 64); err != nil {
			return err
		}
		return writeJSONString(w, v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if err := b.ReserveNodes(1, 64); err != nil {
			return err
		}
		var scratch [64]byte
		_, err := w.Write(strconv.AppendInt(scratch[:0], v.Int(), 10))
		return err
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if err := b.ReserveNodes(1, 64); err != nil {
			return err
		}
		var scratch [64]byte
		_, err := w.Write(strconv.AppendUint(scratch[:0], v.Uint(), 10))
		return err
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("non-finite JSON number")
		}
		if err := b.ReserveNodes(2, MaxNodeAllocationBytes); err != nil {
			return err
		}
		var scratch [64]byte
		_, err := w.Write(strconv.AppendFloat(scratch[:0], f, 'g', -1, v.Type().Bits()))
		return err
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return writeLiteral(w, "null")
		}
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			if err := writeLiteral(w, "\""); err != nil {
				return err
			}
			if err := b.ReserveNodes(1, 64); err != nil {
				return err
			}
			var scratch [4]byte
			d := v.Bytes()
			for len(d) > 0 {
				n := min(3, len(d))
				base64.StdEncoding.Encode(scratch[:], d[:n])
				if _, err := w.Write(scratch[:4]); err != nil {
					return err
				}
				d = d[n:]
			}
			return writeLiteral(w, "\"")
		}
		if err := writeLiteral(w, "["); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				if err := writeLiteral(w, ","); err != nil {
					return err
				}
			}
			if err := b.encodeValue(w, v.Index(i), materialize); err != nil {
				return err
			}
		}
		return writeLiteral(w, "]")
	case reflect.Map:
		if v.IsNil() {
			return writeLiteral(w, "null")
		}
		if v.Type().Key().Kind() != reflect.String {
			return errors.New("JSON map keys must be strings")
		}
		if v.Type().Elem().Size() > uintptr(MaxNodeAllocationBytes) {
			return &BuildCapacityError{"map value reflection size"}
		}
		if err := writeLiteral(w, "{"); err != nil {
			return err
		}
		first := true
		entry := func(k, child reflect.Value) error {
			if b.encodeNodes >= b.nodeLimit {
				return &BuildCapacityError{"encoded JSON key nodes"}
			}
			b.encodeNodes++
			if err := b.ReserveNodes(1, 64); err != nil {
				return err
			}
			if !first {
				if err := writeLiteral(w, ","); err != nil {
					return err
				}
			}
			first = false
			if err := writeJSONString(w, k.String()); err != nil {
				return err
			}
			if err := writeLiteral(w, ":"); err != nil {
				return err
			}
			return b.encodeValue(w, child, materialize)
		}
		if materialize {
			if err := b.ReserveNodes(int64(v.Len())+1, 256); err != nil {
				return err
			}
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
			for _, k := range keys {
				if err := b.ReserveNodes(1, int64(v.Type().Elem().Size())); err != nil {
					return err
				}
				if err := entry(k, v.MapIndex(k)); err != nil {
					return err
				}
			}
		} else {
			iter := v.MapRange()
			for iter.Next() {
				if err := b.ReserveNodes(1, int64(v.Type().Key().Size())+int64(v.Type().Elem().Size())); err != nil {
					return err
				}
				if err := entry(iter.Key(), iter.Value()); err != nil {
					return err
				}
			}
		}
		return writeLiteral(w, "}")
	case reflect.Struct:
		if v.Type().Size() > uintptr(MaxNodeAllocationBytes) {
			return &BuildCapacityError{"struct node size"}
		}
		if err := writeLiteral(w, "{"); err != nil {
			return err
		}
		first := true
		for i := 0; i < v.NumField(); i++ {
			if err := b.ReserveNodes(1, 512); err != nil {
				return err
			}
			field := v.Type().Field(i)
			if len(field.Name) > 128 || len(field.Tag) > 128 {
				return &BuildCapacityError{"JSON field metadata"}
			}
			name, omit, ok := jsonField(field)
			if !ok || omit && v.Field(i).IsZero() {
				continue
			}
			if !first {
				if err := writeLiteral(w, ","); err != nil {
					return err
				}
			}
			first = false
			if b.encodeNodes >= b.nodeLimit {
				return &BuildCapacityError{"encoded JSON key nodes"}
			}
			b.encodeNodes++
			if err := b.ReserveNodes(1, 64); err != nil {
				return err
			}
			if err := writeJSONString(w, name); err != nil {
				return err
			}
			if err := writeLiteral(w, ":"); err != nil {
				return err
			}
			if err := b.encodeValue(w, v.Field(i), materialize); err != nil {
				return err
			}
		}
		return writeLiteral(w, "}")
	default:
		return errors.New("unsupported bounded JSON type")
	}
}

// A scanner node has only offsets and sibling indexes. Raw strings and unknown
// properties never allocate copies. The arena is exact capacity, charged once.
type jsonNode struct {
	start, end, child, next, count int
	kind                           byte
}
type jsonScan struct {
	data              []byte
	pos, count, depth int
	limit             int64
	nodes             []jsonNode
	materialize       bool
	maxDepth          int
}

func scanJSON(data []byte, nodes []jsonNode, limit int64) (int, error) {
	return scanJSONBudget(data, nodes, limit, MaxBuildDepth)
}
func scanJSONBudget(data []byte, nodes []jsonNode, limit int64, depth int) (int, error) {
	s := jsonScan{data: data, limit: limit, nodes: nodes, materialize: nodes != nil, maxDepth: depth}
	_, err := s.value()
	if err != nil {
		return 0, err
	}
	s.space()
	if s.pos != len(data) {
		return 0, errors.New("trailing JSON input")
	}
	return s.count, nil
}
func (s *jsonScan) space() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\n', '\r', '\t':
			s.pos++
		default:
			return
		}
	}
}
func (s *jsonScan) value() (int, error) {
	s.space()
	if s.pos >= len(s.data) {
		return 0, errors.New("incomplete JSON")
	}
	if s.depth >= s.maxDepth {
		return 0, &BuildCapacityError{"JSON depth"}
	}
	s.depth++
	defer func() { s.depth-- }()
	if int64(s.count) >= s.limit {
		return 0, &BuildCapacityError{"JSON nodes"}
	}
	idx := s.count
	s.count++
	start := s.pos
	kind := s.data[s.pos]
	child, last, n := -1, -1, 0
	if s.materialize {
		s.nodes[idx] = jsonNode{start: start, child: -1, next: -1, kind: kind}
	}
	link := func(i int) {
		if child < 0 {
			child = i
		}
		if last >= 0 && s.materialize {
			s.nodes[last].next = i
		}
		last = i
		n++
	}
	switch kind {
	case '"':
		if err := s.stringToken(); err != nil {
			return 0, err
		}
	case '{', '[':
		s.pos++
		s.space()
		close := byte(']')
		if kind == '{' {
			close = '}'
		}
		if s.pos < len(s.data) && s.data[s.pos] == close {
			s.pos++
		} else {
			for {
				if kind == '{' {
					s.space()
					if s.pos >= len(s.data) || s.data[s.pos] != '"' {
						return 0, errors.New("invalid JSON object key")
					}
					k, err := s.value()
					if err != nil {
						return 0, err
					}
					link(k)
					s.space()
					if s.pos >= len(s.data) || s.data[s.pos] != ':' {
						return 0, errors.New("missing JSON colon")
					}
					s.pos++
				}
				i, err := s.value()
				if err != nil {
					return 0, err
				}
				link(i)
				s.space()
				if s.pos >= len(s.data) {
					return 0, errors.New("incomplete JSON container")
				}
				c := s.data[s.pos]
				s.pos++
				if c == close {
					break
				}
				if c != ',' {
					return 0, errors.New("invalid JSON separator")
				}
			}
		}
	case 't', 'f', 'n':
		literal := "null"
		if kind == 't' {
			literal = "true"
		}
		if kind == 'f' {
			literal = "false"
		}
		if len(s.data)-s.pos < len(literal) {
			return 0, errors.New("invalid JSON literal")
		}
		for i := range literal {
			if s.data[s.pos+i] != literal[i] {
				return 0, errors.New("invalid JSON literal")
			}
		}
		s.pos += len(literal)
	default:
		if kind == '-' {
			s.pos++
			if s.pos >= len(s.data) {
				return 0, errors.New("incomplete JSON number")
			}
		}
		if s.data[s.pos] == '0' {
			s.pos++
		} else {
			if s.data[s.pos] < '1' || s.data[s.pos] > '9' {
				return 0, errors.New("invalid JSON number")
			}
			for s.pos < len(s.data) && s.data[s.pos] >= '0' && s.data[s.pos] <= '9' {
				s.pos++
			}
		}
		if s.pos < len(s.data) && s.data[s.pos] == '.' {
			s.pos++
			at := s.pos
			for s.pos < len(s.data) && s.data[s.pos] >= '0' && s.data[s.pos] <= '9' {
				s.pos++
			}
			if at == s.pos {
				return 0, errors.New("invalid JSON fraction")
			}
		}
		if s.pos < len(s.data) && (s.data[s.pos] == 'e' || s.data[s.pos] == 'E') {
			s.pos++
			if s.pos < len(s.data) && (s.data[s.pos] == '+' || s.data[s.pos] == '-') {
				s.pos++
			}
			at := s.pos
			for s.pos < len(s.data) && s.data[s.pos] >= '0' && s.data[s.pos] <= '9' {
				s.pos++
			}
			if at == s.pos {
				return 0, errors.New("invalid JSON exponent")
			}
		}
	}
	if s.materialize {
		s.nodes[idx].end = s.pos
		s.nodes[idx].child = child
		s.nodes[idx].count = n
	}
	return idx, nil
}
func (s *jsonScan) stringToken() error {
	s.pos++
	for s.pos < len(s.data) {
		c := s.data[s.pos]
		s.pos++
		if c == '"' {
			return nil
		}
		if c < 0x20 {
			return errors.New("JSON string control character")
		}
		if c == '\\' {
			if s.pos >= len(s.data) {
				return errors.New("incomplete JSON escape")
			}
			e := s.data[s.pos]
			s.pos++
			switch e {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if s.pos+4 > len(s.data) {
					return errors.New("incomplete JSON unicode")
				}
				for _, h := range s.data[s.pos : s.pos+4] {
					if !isHex(h) {
						return errors.New("invalid JSON unicode")
					}
				}
				s.pos += 4
			default:
				return errors.New("invalid JSON escape")
			}
		}
		if c >= utf8.RuneSelf {
			r, n := utf8.DecodeRune(s.data[s.pos-1:])
			if r == utf8.RuneError && n == 1 {
				return errors.New("invalid JSON UTF-8")
			}
			s.pos += n - 1
		}
	}
	return errors.New("incomplete JSON string")
}
func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
func hexRune(d []byte) rune {
	var n rune
	for _, c := range d {
		n *= 16
		switch {
		case c <= '9':
			n += rune(c - '0')
		case c <= 'F':
			n += rune(c - 'A' + 10)
		default:
			n += rune(c - 'a' + 10)
		}
	}
	return n
}

func (b *BuildBounds) stringValue(d []byte) (string, error) {
	if len(d) < 2 || d[0] != '"' || d[len(d)-1] != '"' {
		return "", errors.New("expected JSON string")
	}
	// Count decoded UTF-8 without allocating so a legal large response cannot
	// allocate a target above Q first. Escaped input does not inflate capacity.
	n := 0
	for i := 1; i < len(d)-1; i++ {
		if d[i] != '\\' {
			n++
			continue
		}
		i++
		if d[i] != 'u' {
			n++
			continue
		}
		r := hexRune(d[i+1 : i+5])
		i += 4
		if utf16.IsSurrogate(r) {
			if r >= 0xD800 && r <= 0xDBFF && i+6 < len(d)-1 && d[i+1] == '\\' && d[i+2] == 'u' {
				r2 := hexRune(d[i+3 : i+7])
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
		n += utf8.RuneLen(r)
	}
	if int64(n) > b.stringLimit {
		return "", &BuildCapacityError{"decoded string bytes"}
	}
	if err := b.ReserveText(int64(n), int64(n)); err != nil {
		return "", err
	}
	out := make([]byte, 0, n)
	for i := 1; i < len(d)-1; i++ {
		c := d[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		e := d[i]
		switch e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r := hexRune(d[i+1 : i+5])
			i += 4
			if utf16.IsSurrogate(r) {
				if r >= 0xD800 && r <= 0xDBFF && i+6 < len(d)-1 && d[i+1] == '\\' && d[i+2] == 'u' {
					r2 := hexRune(d[i+3 : i+7])
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
			out = utf8.AppendRune(out, r)
		}
	}
	if err := b.ReserveText(int64(len(out)), int64(len(out))); err != nil {
		return "", err
	}
	return string(out), nil
}

func (b *BuildBounds) DecodeJSON(data []byte, v any) error {
	dst := reflect.ValueOf(v)
	if !dst.IsValid() || dst.Kind() != reflect.Pointer || dst.IsNil() {
		return errors.New("bounded JSON destination must be a nonnil pointer")
	}
	n, err := scanJSON(data, nil, b.nodeLimit)
	if err != nil {
		return err
	}
	if err = b.ReserveNodes(int64(n), int64(unsafe.Sizeof(jsonNode{}))); err != nil {
		return err
	}
	nodes := make([]jsonNode, n)
	if _, err = scanJSON(data, nodes, b.nodeLimit); err != nil {
		return err
	}
	return b.decodeValue(data, nodes, 0, dst.Elem())
}

func (b *BuildBounds) decodeValue(data []byte, nodes []jsonNode, i int, v reflect.Value) error {
	n := nodes[i]
	raw := data[n.start:n.end]
	if v.Type() == rawJSONType {
		if err := b.ReserveText(int64(len(raw)), int64(len(raw))); err != nil {
			return err
		}
		out := make([]byte, len(raw))
		copy(out, raw)
		v.SetBytes(out)
		return nil
	}
	if n.kind == 'n' {
		v.SetZero()
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if err := b.ReserveNodes(1, int64(v.Type().Elem().Size())); err != nil {
			return err
		}
		v.Set(reflect.New(v.Type().Elem()))
		return b.decodeValue(data, nodes, i, v.Elem())
	}
	if v.Type() == timeType {
		if len(raw) > 66 {
			return &BuildCapacityError{"RFC3339 time token"}
		}
		s, err := b.stringValue(raw)
		if err != nil {
			return err
		}
		if err := b.ReserveNodes(2, MaxNodeAllocationBytes); err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return errors.New("invalid bounded RFC3339 time")
		}
		v.Set(reflect.ValueOf(t))
		return nil
	}
	if v.Kind() == reflect.Interface {
		if v.NumMethod() != 0 {
			return errors.New("unsupported bounded JSON interface")
		}
		var concrete reflect.Value
		if err := b.ReserveNodes(1, 64); err != nil {
			return err
		}
		switch n.kind {
		case '{':
			concrete = reflect.New(reflect.TypeFor[map[string]any]()).Elem()
		case '[':
			concrete = reflect.New(reflect.TypeFor[[]any]()).Elem()
		case '"':
			concrete = reflect.New(reflect.TypeFor[string]()).Elem()
		case 't', 'f':
			concrete = reflect.New(reflect.TypeFor[bool]()).Elem()
		default:
			concrete = reflect.New(numberType).Elem()
		}
		if err := b.decodeValue(data, nodes, i, concrete); err != nil {
			return err
		}
		v.Set(concrete)
		return nil
	}
	switch v.Kind() {
	case reflect.Struct:
		if n.kind != '{' {
			return errors.New("expected JSON object")
		}
		if v.Type().Size() > uintptr(MaxNodeAllocationBytes) {
			return &BuildCapacityError{"decoded struct node"}
		}
		fields, fieldErr := b.fieldsFor(v.Type())
		if fieldErr != nil {
			return fieldErr
		}
		for c := n.child; c >= 0; {
			key, err := b.stringValue(data[nodes[c].start:nodes[c].end])
			if err != nil {
				return err
			}
			value := nodes[c].next
			found := -1
			for _, field := range fields {
				if field.name == key {
					found = field.index
					break
				}
			}
			if found >= 0 {
				if err = b.decodeValue(data, nodes, value, v.Field(found)); err != nil {
					return err
				}
			}
			c = nodes[value].next
		}
		return nil
	case reflect.Map:
		if n.kind != '{' || v.Type().Key().Kind() != reflect.String {
			return errors.New("expected string-key JSON object")
		}
		count := n.count / 2
		if err := b.ReserveNodes(int64(count)+1, MaxNodeAllocationBytes); err != nil {
			return err
		}
		v.Set(reflect.MakeMapWithSize(v.Type(), count))
		for c := n.child; c >= 0; {
			key, err := b.stringValue(data[nodes[c].start:nodes[c].end])
			if err != nil {
				return err
			}
			value := nodes[c].next
			if err = b.ReserveNodes(1, int64(v.Type().Elem().Size())); err != nil {
				return err
			}
			elem := reflect.New(v.Type().Elem()).Elem()
			if err = b.decodeValue(data, nodes, value, elem); err != nil {
				return err
			}
			if err := b.ReserveNodes(1, int64(v.Type().Key().Size())); err != nil {
				return err
			}
			k := reflect.New(v.Type().Key()).Elem()
			k.SetString(key)
			if v.Type().Elem().Size() > 128 {
				if err := b.ReserveNodes(1, int64(v.Type().Elem().Size())); err != nil {
					return err
				}
			}
			v.SetMapIndex(k, elem)
			c = nodes[value].next
		}
		return nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 && n.kind == '"' {
			s, err := b.stringValue(raw)
			if err != nil {
				return err
			}
			length := base64.StdEncoding.DecodedLen(len(s))
			if err = b.ReserveText(int64(length), int64(length)); err != nil {
				return err
			}
			out := make([]byte, length)
			if err = b.ReserveText(int64(len(s)), int64(len(s))); err != nil {
				return err
			}
			input := make([]byte, len(s))
			copy(input, s)
			written, err := base64.StdEncoding.Decode(out, input)
			if err != nil {
				return err
			}
			v.SetBytes(out[:written])
			return nil
		}
		if n.kind != '[' {
			return errors.New("expected JSON array")
		}
		if v.Kind() == reflect.Slice {
			if err := b.ReserveNodes(1, int64(v.Type().Size())); err != nil {
				return err
			}
			if err := b.ReserveNodes(int64(n.count), int64(v.Type().Elem().Size())); err != nil {
				return err
			}
			v.Set(reflect.MakeSlice(v.Type(), n.count, n.count))
		} else if v.Len() != n.count {
			return errors.New("JSON array length mismatch")
		}
		j := 0
		for c := n.child; c >= 0; c = nodes[c].next {
			if err := b.decodeValue(data, nodes, c, v.Index(j)); err != nil {
				return err
			}
			j++
		}
		return nil
	case reflect.String:
		if v.Type() == numberType {
			if n.kind == '"' || n.kind == '{' || n.kind == '[' || n.kind == 't' || n.kind == 'f' {
				return errors.New("expected JSON number")
			}
			if err := b.ReserveText(int64(len(raw)), int64(len(raw))); err != nil {
				return err
			}
			v.SetString(string(raw))
			return nil
		}
		if n.kind != '"' {
			return errors.New("expected JSON string")
		}
		s, err := b.stringValue(raw)
		if err != nil {
			return err
		}
		v.SetString(s)
		return nil
	case reflect.Bool:
		if n.kind != 't' && n.kind != 'f' {
			return errors.New("expected JSON boolean")
		}
		v.SetBool(n.kind == 't')
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if len(raw) > 64 {
			return &BuildCapacityError{"numeric token"}
		}
		if err := b.ReserveNodes(2, MaxNodeAllocationBytes); err != nil {
			return err
		}
		x, err := strconv.ParseInt(string(raw), 10, v.Type().Bits())
		if err != nil {
			return errors.New("invalid bounded integer")
		}
		v.SetInt(x)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if len(raw) > 64 {
			return &BuildCapacityError{"numeric token"}
		}
		if err := b.ReserveNodes(2, MaxNodeAllocationBytes); err != nil {
			return err
		}
		x, err := strconv.ParseUint(string(raw), 10, v.Type().Bits())
		if err != nil {
			return errors.New("invalid bounded unsigned integer")
		}
		v.SetUint(x)
		return nil
	case reflect.Float32, reflect.Float64:
		if len(raw) > 64 {
			return &BuildCapacityError{"numeric token"}
		}
		if err := b.ReserveNodes(2, MaxNodeAllocationBytes); err != nil {
			return err
		}
		x, err := strconv.ParseFloat(string(raw), v.Type().Bits())
		if err != nil {
			return errors.New("invalid bounded number")
		}
		v.SetFloat(x)
		return nil
	default:
		return errors.New("unsupported bounded JSON destination")
	}
}
