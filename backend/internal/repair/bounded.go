package repair

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func limitText(b *backend.BuildBounds) int64 {
	if b.OutputLimit() > 0 {
		return b.OutputLimit()
	}
	return b.Snapshot().TextLimit
}
func copyBytesBounded(b *backend.BuildBounds, s string) ([]byte, error) {
	if e := b.ReserveText(int64(len(s)), int64(len(s))); e != nil {
		return nil, e
	}
	data := make([]byte, len(s))
	copy(data, s)
	return data, nil
}
func decodeGenericBounded(b *backend.BuildBounds, s string) (map[string]any, error) {
	data, e := copyBytesBounded(b, s)
	if e != nil {
		return nil, e
	}
	var raw map[string]any
	if e = b.DecodeJSONLimited(data, &raw, limitText(b)); e != nil {
		return nil, e
	}
	v, e := backend.NewJSONView(b, data)
	if e != nil {
		return nil, e
	}
	if e = preserveRubyView(b, v, raw); e != nil {
		return nil, e
	}
	return raw, nil
}
func preserveRubyView(b *backend.BuildBounds, v backend.JSONValue, raw map[string]any) error {
	if v.Kind() != '{' {
		return nil
	}
	duplicate := false
	var rubyValue backend.JSONValue
	e := v.EachField(func(key, value backend.JSONValue) error {
		name, e := key.String()
		if e != nil {
			return e
		}
		if name == "ruby_output" {
			if rubyValue.Kind() != 0 {
				duplicate = true
			}
			rubyValue = value
		} else if child, ok := raw[name].(map[string]any); ok {
			return preserveRubyView(b, value, child)
		}
		return nil
	})
	if e != nil {
		return e
	}
	if rubyValue.Kind() != 0 {
		if duplicate {
			raw["ruby_output"] = nil
		} else {
			r, e := rawRubyView(b, rubyValue)
			if e != nil {
				return e
			}
			raw["ruby_output"] = r
		}
	}
	return nil
}
func rawRubyView(b *backend.BuildBounds, v backend.JSONValue) (any, error) {
	if e := b.ReserveNodes(2, 24); e != nil {
		return nil, e
	}
	if v.Kind() != '[' {
		return json.RawMessage(v.Raw()), nil
	}
	if e := b.ReserveNodes(int64(v.Count()), 16); e != nil {
		return nil, e
	}
	out := make([]any, 0, v.Count())
	e := v.EachArray(func(row backend.JSONValue) error {
		if e := b.ReserveNodes(1, 32); e != nil {
			return e
		}
		out = append(out, json.RawMessage(row.Raw()))
		return nil
	})
	return out, e
}

func cleanZeroWidthBounded(b *backend.BuildBounds, s string) (string, bool, error) {
	orig := s
	for _, ch := range []string{"\uFEFF", "\u200B", "\u200C", "\u200D"} {
		count := strings.Count(s, ch)
		if count == 0 {
			continue
		}
		n := len(s) - count*len(ch)
		if e := b.ReserveText(int64(n), int64(n)); e != nil {
			return "", false, e
		}
		s = strings.ReplaceAll(s, ch, "")
	}
	return s, s != orig, nil
}
func jsonDepthBounded(s string) error {
	var stack [64]byte
	n := 0
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			if n == len(stack) {
				return &backend.BuildCapacityError{Resource: "repair JSON depth"}
			}
			stack[n] = c
			n++
		case '}', ']':
			if n > 0 {
				n--
			}
		}
	}
	return nil
}
func fixJSONBounded(b *backend.BuildBounds, s string, kind int) (string, error) {
	if e := jsonDepthBounded(s); e != nil {
		return "", e
	}
	if kind != 3 && !jsonFixNeeded(s, kind) {
		return s, nil
	}
	w := backend.NewBoundedWriter(b, b.Snapshot().TextLimit)
	write := func(c byte) error { var one [1]byte; one[0] = c; _, e := w.Write(one[:]); return e }
	inString, escaped := false, false
	if kind == 3 {
		var stack [64]byte
		n := 0
		for i := 0; i < len(s); i++ {
			c := s[i]
			if inString {
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					inString = false
				}
				continue
			}
			switch c {
			case '"':
				inString = true
			case '{', '[':
				if n == 64 {
					return "", &backend.BuildCapacityError{Resource: "repair JSON depth"}
				}
				stack[n] = c
				n++
			case '}', ']':
				if n > 0 && ((c == '}' && stack[n-1] == '{') || (c == ']' && stack[n-1] == '[')) {
					n--
				}
			}
		}
		if inString || n == 0 {
			return s, nil
		}
		if _, e := w.WriteString(s); e != nil {
			return "", e
		}
		for i := n - 1; i >= 0; i-- {
			c := byte('}')
			if stack[i] == '[' {
				c = ']'
			}
			if e := write(c); e != nil {
				return "", e
			}
		}
	} else {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if inString {
				if escaped {
					if e := write(c); e != nil {
						return "", e
					}
					escaped = false
					continue
				}
				if c == '\\' {
					if e := write(c); e != nil {
						return "", e
					}
					escaped = true
					continue
				}
				if c == '"' {
					if kind == 2 && !isLegalStringTerminator(s, i+1) {
						if _, e := w.WriteString(`\"`); e != nil {
							return "", e
						}
						continue
					}
					inString = false
				} else if kind == 1 && c < 0x20 {
					const hex = "0123456789ABCDEF"
					var esc = [6]byte{'\\', 'u', '0', '0', hex[c>>4], hex[c&15]}
					if _, e := w.Write(esc[:]); e != nil {
						return "", e
					}
					continue
				}
				if e := write(c); e != nil {
					return "", e
				}
				continue
			}
			if c == '"' {
				inString = true
			}
			if kind == 0 && c == ',' {
				j := i + 1
				for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\r' || s[j] == '\t') {
					j++
				}
				if j < len(s) && (s[j] == '}' || s[j] == ']') {
					continue
				}
			}
			if e := write(c); e != nil {
				return "", e
			}
		}
	}
	if e := b.ReserveText(int64(len(w.Bytes())), int64(len(w.Bytes()))); e != nil {
		return "", e
	}
	return string(w.Bytes()), nil
}
func jsonFixNeeded(s string, kind int) bool {
	quoted, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				if kind == 2 && !isLegalStringTerminator(s, i+1) {
					return true
				}
				quoted = false
			} else if kind == 1 && c < 32 {
				return true
			}
			continue
		}
		if c == '"' {
			quoted = true
		}
		if kind == 0 && c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\r' || s[j] == '\t') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				return true
			}
		}
	}
	return false
}

func repairEnvelopeBounded(text, key string, opt Options, array bool) (map[string]any, []string, error) {
	b := opt.Bounds
	if len(key) > 64 {
		return nil, nil, &backend.BuildCapacityError{Resource: "repair envelope key"}
	}
	if opt.BareArrayAccept != nil {
		pointer := reflect.ValueOf(opt.BareArrayAccept).Pointer()
		if pointer != reflect.ValueOf(ruby.ValidBareOutputEntries).Pointer() && pointer != reflect.ValueOf(prompt.ValidBareVerdictEntries).Pointer() {
			return nil, nil, &backend.BuildCapacityError{Resource: "unsupported repair callback"}
		}
	}
	if e := b.ReserveNodes(32, 64); e != nil {
		return nil, nil, e
	}
	if e := jsonDepthBounded(text); e != nil {
		return nil, nil, e
	}
	repaired := make([]string, 0, 16)
	if opt.JSONStructural {
		cleaned, did, e := cleanZeroWidthBounded(b, text)
		if e != nil {
			return nil, repaired, e
		}
		if did {
			repaired = append(repaired, "json.strip-bom-zw")
		}
		text = cleaned
	}
	body := extractJSONObjectContaining(text, key)
	if !array && body == "" {
		body = jsonObjectSlice(text)
	}
	if body == "" && opt.JSONStructural && !opt.salvageDeclined {
		fixed, e := fixJSONBounded(b, text, 3)
		if e != nil {
			return nil, repaired, e
		}
		if fixed != text {
			repaired = append(repaired, "json.close-braces")
			body = extractJSONObjectContaining(fixed, key)
			if !array && body == "" {
				body = jsonObjectSlice(fixed)
			}
		}
	}
	raw, err := decodeGenericBounded(b, body)
	if errors.Is(err, backend.ErrBuildCapacity) {
		return nil, repaired, err
	}
	if err != nil && opt.JSONStructural {
		fixed := body
		for kind, op := range []string{"json.trailing-comma", "json.escape-control", "json.escape-quotes", "json.close-braces"} {
			v, e := fixJSONBounded(b, fixed, kind)
			if e != nil {
				return nil, repaired, e
			}
			if v != fixed {
				fixed = v
				repaired = append(repaired, op)
			}
		}
		if fixed != body {
			raw, err = decodeGenericBounded(b, fixed)
			if errors.Is(err, backend.ErrBuildCapacity) {
				return nil, repaired, err
			}
		}
	}
	accept := func(m map[string]any) bool {
		if !array {
			return true
		}
		switch m[key].(type) {
		case []any:
			return true
		}
		return false
	}
	if err == nil && !accept(raw) {
		err = errors.New("response field is not an array")
	}
	if err != nil && opt.JSONStructural {
		for attempts, i := 0, 0; i < len(text) && attempts < robustScanMaxAttempts; i++ {
			if text[i] != '{' {
				continue
			}
			attempts++
			tail := text[i:]
			data, e := copyBytesBounded(b, tail)
			if e != nil {
				return nil, repaired, e
			}
			value, _, e := backend.FirstJSONRaw(b, data)
			if e != nil && !opt.salvageDeclined && len(tail) <= robustScanTruncationWindow {
				fixed, e2 := fixJSONBounded(b, tail, 3)
				if e2 != nil {
					return nil, repaired, e2
				}
				if fixed != tail {
					data, e2 = copyBytesBounded(b, fixed)
					if e2 != nil {
						return nil, repaired, e2
					}
					value, _, e = backend.FirstJSONRaw(b, data)
				}
			}
			if errors.Is(e, backend.ErrBuildCapacity) {
				return nil, repaired, e
			}
			if e != nil {
				continue
			}
			if e = b.ReserveText(int64(len(value)), int64(len(value))); e != nil {
				return nil, repaired, e
			}
			candidate, e := decodeGenericBounded(b, string(value))
			if errors.Is(e, backend.ErrBuildCapacity) {
				return nil, repaired, e
			}
			if e == nil && candidate[key] != nil && accept(candidate) {
				return candidate, []string{"json.robust-extract"}, nil
			}
		}
	}
	if err != nil && array && opt.JSONStructural {
		for attempts, i := 0, 0; i < len(text) && attempts < robustScanMaxAttempts; i++ {
			if text[i] != '[' {
				continue
			}
			attempts++
			data, e := copyBytesBounded(b, text[i:])
			if e != nil {
				return nil, repaired, e
			}
			value, _, e := backend.FirstJSONRaw(b, data)
			if e != nil && !opt.salvageDeclined && len(text)-i <= robustScanTruncationWindow {
				fixed, e2 := fixJSONBounded(b, text[i:], 3)
				if e2 != nil {
					return nil, repaired, e2
				}
				data, e2 = copyBytesBounded(b, fixed)
				if e2 != nil {
					return nil, repaired, e2
				}
				value, _, e = backend.FirstJSONRaw(b, data)
			}
			if errors.Is(e, backend.ErrBuildCapacity) {
				return nil, repaired, e
			}
			if e != nil {
				continue
			}
			var arr []any
			if e = b.DecodeJSONLimited(value, &arr, limitText(b)); e != nil {
				if errors.Is(e, backend.ErrBuildCapacity) {
					return nil, repaired, e
				}
				continue
			}
			if !allObjectEntries(arr) || (opt.BareArrayAccept != nil && !opt.BareArrayAccept(arr)) {
				continue
			}
			if e = b.ReserveNodes(1, 1024); e != nil {
				return nil, repaired, e
			}
			if key == "ruby_output" {
				view, e := backend.NewJSONView(b, value)
				if e != nil {
					return nil, repaired, e
				}
				rawValue, e := rawRubyView(b, view)
				if e != nil {
					return nil, repaired, e
				}
				return map[string]any{key: rawValue}, []string{"json.bare-array"}, nil
			}
			return map[string]any{key: arr}, []string{"json.bare-array"}, nil
		}
	}
	if err != nil && opt.JSONStructural && !opt.salvageDeclined && !opt.inSalvage {
		cut, ok := truncateToLastCompleteValue(text)
		if ok {
			raw2, ops, e := repairEnvelopeBounded(cut, key, opt.enterSalvage(), array)
			if e == nil {
				return raw2, append(append(repaired, "json.truncation-salvage"), ops...), nil
			}
			if errors.Is(e, backend.ErrBuildCapacity) {
				return nil, repaired, e
			}
		}
	}
	if err != nil {
		if body == "" {
			return nil, repaired, errors.New("no JSON object found")
		}
		return nil, repaired, err
	}
	return raw, repaired, nil
}

func convertBounded(b *backend.BuildBounds, v any, dst any) error {
	data, e := b.EncodeJSON(v, b.Snapshot().TextLimit)
	if e != nil {
		return e
	}
	return b.DecodeJSONLimited(data, dst, limitText(b))
}
func transMapBounded(b *backend.BuildBounds, v any) (map[string]string, error) {
	count := 0
	switch tv := v.(type) {
	case map[string]any:
		count = len(tv)
	case []any:
		count = len(tv)
	default:
		return nil, errors.New("expected translations object or array")
	}
	if e := b.ReserveNodes(int64(count)+1, 1024); e != nil {
		return nil, e
	}
	out := make(map[string]string, count)
	read := func(value any) (string, error) {
		switch val := value.(type) {
		case string:
			return val, nil
		case map[string]any:
			if s := firstStringKey(val, "target", "translation", "text", "source"); s != "" {
				return s, nil
			}
		}
		return "", errors.New("translation value has no translatable string")
	}
	switch val := v.(type) {
	case map[string]any:
		for id, item := range val {
			s, e := read(item)
			if e != nil {
				return nil, e
			}
			out[id] = s
		}
	case []any:
		for _, item := range val {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("translation array item is not an object")
			}
			id, _ := row["id"].(string)
			if id == "" {
				return nil, errors.New("translation array item has no string id")
			}
			s, e := read(row)
			if e != nil {
				return nil, e
			}
			out[id] = s
		}
	}
	return out, nil
}
func finalizeBounded(raw map[string]any, wantIDs, repaired []string, opt Options) Result {
	b := opt.Bounds
	if opt.SchemaAliases {
		if e := b.ReserveNodes(1, 1024); e != nil {
			return Result{Fatal: true, ParseErr: e}
		}
		if m, did := normalizeEnvelopeKeys(raw); did {
			raw = m
			repaired = append(repaired, "schema.alias")
		}
	}
	v, ok := raw["translations"]
	if !ok {
		return Result{Fatal: true, Repaired: repaired, ParseErr: errors.New("response missing translations field")}
	}
	trans, e := transMapBounded(b, v)
	if e != nil {
		return Result{Fatal: true, Repaired: repaired, ParseErr: e}
	}
	var glos []prompt.BootstrapEntry
	if value, ok := raw["glossary"]; ok {
		if e = convertBounded(b, value, &glos); errors.Is(e, backend.ErrBuildCapacity) {
			return Result{Fatal: true, Repaired: repaired, ParseErr: e}
		}
	}
	var output map[string][]ruby.OutputEntry
	if value, ok := raw["ruby_output"]; ok {
		data, e := b.EncodeJSON(value, b.Snapshot().TextLimit)
		if e != nil {
			return Result{Fatal: true, Repaired: repaired, ParseErr: e}
		}
		output, e = ruby.ParseOutputMapBounded(b, data)
		if errors.Is(e, backend.ErrBuildCapacity) {
			return Result{Fatal: true, Repaired: repaired, ParseErr: e}
		}
	}
	if output == nil {
		if tv, ok := v.(map[string]any); ok {
			if e = b.ReserveNodes(int64(len(tv))+1, 1024); e != nil {
				return Result{Fatal: true, ParseErr: e}
			}
			for id, value := range tv {
				m, ok := value.(map[string]any)
				if !ok {
					continue
				}
				if rows, ok := m["ruby_output"]; ok {
					data, e := b.EncodeJSON(rows, b.Snapshot().TextLimit)
					if e != nil {
						return Result{Fatal: true, ParseErr: e}
					}
					parsed, e := ruby.ParseOutputEntriesBounded(b, data)
					if errors.Is(e, backend.ErrBuildCapacity) {
						return Result{Fatal: true, ParseErr: e}
					}
					if e == nil {
						if output == nil {
							output = make(map[string][]ruby.OutputEntry, len(tv))
						}
						output[id] = parsed
					}
				}
			}
			if len(output) > 0 {
				repaired = append(repaired, "schema.ruby-nested-extract")
			}
		}
	}
	if e = b.ReserveNodes(int64(len(wantIDs)), 16); e != nil {
		return Result{Fatal: true, ParseErr: e}
	}
	missing := make([]string, 0, len(wantIDs))
	for _, id := range wantIDs {
		if _, ok := trans[id]; !ok {
			missing = append(missing, id)
		}
	}
	return Result{Trans: trans, Glos: glos, RubyOutput: output, Missing: missing, Repaired: repaired}
}

func tryRepairBounded(text string, wantIDs []string, opt Options) Result {
	// The canonical multi-object merge preserves first strings just like the
	// existing structural repair. All decoded maps are credited independently.
	if opt.JSONStructural {
		b := opt.Bounds
		if e := b.ReserveNodes(2, 1024); e != nil {
			return Result{Fatal: true, ParseErr: e}
		}
		merged := make(map[string]any)
		var first map[string]any
		count := 0
		for from := 0; from < len(text); {
			off := strings.IndexByte(text[from:], '{')
			if off < 0 {
				break
			}
			start := from + off
			end := matchBracePair(text, start)
			if end < 0 {
				break
			}
			from = end + 1
			raw, e := decodeGenericBounded(b, text[start:from])
			if errors.Is(e, backend.ErrBuildCapacity) {
				return Result{Fatal: true, ParseErr: e}
			}
			if e != nil {
				continue
			}
			m, ok := raw["translations"].(map[string]any)
			if !ok {
				continue
			}
			count++
			if first == nil {
				first = raw
			}
			for k, v := range m {
				if _, ok := v.(string); !ok {
					continue
				}
				if _, exists := merged[k]; exists {
					continue
				}
				if e = b.ReserveNodes(1, 1024); e != nil {
					return Result{Fatal: true, ParseErr: e}
				}
				merged[k] = v
			}
		}
		if count >= 2 {
			first["translations"] = merged
			return finalizeBounded(first, wantIDs, []string{"json.merge-objects"}, opt)
		}
	}
	raw, ops, e := repairEnvelopeBounded(text, "translations", opt, false)
	if e != nil {
		return Result{Fatal: true, Repaired: ops, ParseErr: e}
	}
	return finalizeBounded(raw, wantIDs, ops, opt)
}

func tryRepairBootstrapBounded(text string, opt Options) ([]prompt.BootstrapEntry, []string, error) {
	key := "glossary"
	if opt.SchemaAliases {
		for _, k := range []string{"glossary", "terms", "entries"} {
			if extractJSONObjectContaining(text, k) != "" {
				key = k
				break
			}
		}
	}
	raw, ops, e := repairEnvelopeBounded(text, key, opt, false)
	if e != nil {
		return nil, ops, e
	}
	value, ok := raw[key]
	if !ok {
		return nil, ops, errors.New("response missing glossary field")
	}
	if key != "glossary" {
		ops = append(ops, "schema.alias")
	}
	var entries []prompt.BootstrapEntry
	if e = convertBounded(opt.Bounds, value, &entries); e != nil {
		return nil, ops, e
	}
	if e = opt.Bounds.ReserveNodes(int64(len(entries))+1, 1024); e != nil {
		return nil, ops, e
	}
	seen := make(map[string]bool, len(entries))
	out := entries[:0]
	for _, entry := range entries {
		entry.Source = strings.TrimSpace(entry.Source)
		entry.Target = strings.TrimSpace(entry.Target)
		entry.Notes = strings.TrimSpace(entry.Notes)
		if entry.Source == "" || entry.Target == "" || seen[entry.Source] {
			continue
		}
		seen[entry.Source] = true
		out = append(out, entry)
	}
	return out, ops, nil
}

func decodeKeyedBounded[T any](raw map[string]any, key string, opt Options, normalize func([]T) []T) ([]T, error) {
	v, ok := raw[key]
	if !ok {
		return nil, errors.New("response missing field")
	}
	var typed []T
	if e := convertBounded(opt.Bounds, v, &typed); e != nil {
		return nil, e
	}
	var zero T
	if e := opt.Bounds.ReserveNodes(int64(len(typed))+1, int64(reflect.TypeOf(zero).Size())); e != nil {
		return nil, e
	}
	if e := opt.Bounds.ReserveNodes(int64(len(typed))+1, 1024); e != nil {
		return nil, e
	}
	return normalize(typed), nil
}
