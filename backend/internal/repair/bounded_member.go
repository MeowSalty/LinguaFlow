package repair

import (
	"errors"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// ParseMainMemberBounded decodes only the selected member. An empty wantedID
// selects the business glossary attachment. Other target strings remain raw
// views, so their size cannot consume the selected member's Q allocation.
func ParseMainMemberBounded(b *backend.BuildBounds, text string, textMode bool, wantIDs []string, wantedID string, opt Options) (Result, error) {
	opt.Bounds = b
	if textMode {
		return parseTextMemberBounded(b, text, wantedID, opt)
	}
	view, ops, err := mainViewBounded(b, text, opt)
	if err != nil {
		return Result{Fatal: true, ParseErr: err, Repaired: ops}, err
	}
	if err = b.ReserveNodes(3, 1024); err != nil {
		return Result{}, err
	}
	r := Result{Trans: make(map[string]string, 1), Repaired: ops}
	if wantedID == "" {
		glossary, e := mainField(view, "glossary")
		if e != nil {
			return r, e
		}
		if glossary.Kind() != 0 {
			e = glossary.DecodeInto(&r.Glos, b.Snapshot().TextLimit)
			if errors.Is(e, backend.ErrBuildCapacity) {
				return r, e
			}
		}
		return r, nil
	}
	translations, e := mainField(view, "translations")
	if e != nil {
		return r, e
	}
	if translations.Kind() == 0 && opt.SchemaAliases {
		for _, alias := range [4]string{"translation", "result", "output", "results"} {
			v, e := mainField(view, alias)
			if e != nil {
				return r, e
			}
			if v.Kind() == '{' {
				translations = v
				break
			}
		}
		if translations.Kind() == 0 {
			data, e := mainField(view, "data")
			if e != nil {
				return r, e
			}
			translations, e = mainField(data, "translations")
			if e != nil {
				return r, e
			}
		}
		if translations.Kind() != 0 {
			r.Repaired, e = backend.AppendNode(b, r.Repaired, "schema.alias")
			if e != nil {
				return r, e
			}
		}
	}
	if translations.Kind() == 0 {
		return Result{Fatal: true, ParseErr: errors.New("response missing translations field")}, errors.New("response missing translations field")
	}
	var selected backend.JSONValue
	switch translations.Kind() {
	case '{':
		selected, e = mainField(translations, wantedID)
	case '[':
		e = translations.EachArray(func(row backend.JSONValue) error {
			id, e := mainField(row, "id")
			if e != nil {
				return e
			}
			if id.Kind() != '"' {
				return nil
			}
			if id.StringEquals(wantedID) {
				selected = row
			}
			return nil
		})
	default:
		e = errors.New("expected translations object or array")
	}
	if e != nil {
		return r, e
	}
	if selected.Kind() == 0 {
		r.Missing, e = backend.AppendNode(b, r.Missing, wantedID)
		return r, e
	}
	target := selected
	if selected.Kind() == '{' {
		for _, name := range [4]string{"target", "translation", "text", "source"} {
			v, e := mainField(selected, name)
			if e != nil {
				return r, e
			}
			if v.Kind() == '"' {
				s, e := v.StringLimited(limitText(b))
				if e != nil {
					return r, e
				}
				if s != "" {
					r.Trans[wantedID] = s
					target = backend.JSONValue{}
					break
				}
			}
		}
	}
	if target.Kind() == '"' {
		s, e := target.StringLimited(limitText(b))
		if e != nil {
			return r, e
		}
		r.Trans[wantedID] = s
	} else if target.Kind() != 0 {
		return r, errors.New("selected translation has no translatable string")
	}
	if _, ok := r.Trans[wantedID]; !ok {
		return r, errors.New("selected translation has no translatable string")
	}
	var rows backend.JSONValue
	output, e := mainField(view, "ruby_output")
	if e != nil {
		return r, e
	}
	if output.Kind() == '{' {
		rows, e = uniqueMainField(output, wantedID)
		if e != nil {
			return r, e
		}
	}
	if rows.Kind() == 0 && selected.Kind() == '{' {
		rows, e = uniqueMainField(selected, "ruby_output")
		if e != nil {
			return r, e
		}
	}
	if rows.Kind() != 0 {
		entries, e := ruby.ParseOutputEntriesBounded(b, rows.Raw())
		if errors.Is(e, backend.ErrBuildCapacity) {
			return r, e
		}
		if e == nil {
			r.RubyOutput = map[string][]ruby.OutputEntry{wantedID: entries}
		}
	}
	return r, nil
}
func mainField(v backend.JSONValue, name string) (backend.JSONValue, error) {
	var out backend.JSONValue
	if v.Kind() != '{' {
		return out, nil
	}
	e := v.EachField(func(k, value backend.JSONValue) error {
		if k.StringEquals(name) {
			out = value
		}
		return nil
	})
	return out, e
}
func uniqueMainField(v backend.JSONValue, name string) (backend.JSONValue, error) {
	var out backend.JSONValue
	seen := false
	duplicate := false
	if v.Kind() != '{' {
		return out, nil
	}
	e := v.EachField(func(k, value backend.JSONValue) error {
		if k.StringEquals(name) {
			if seen {
				duplicate = true
			}
			seen = true
			out = value
		}
		return nil
	})
	if duplicate {
		out = backend.JSONValue{}
	}
	return out, e
}
func mainViewBounded(b *backend.BuildBounds, text string, opt Options) (backend.JSONValue, []string, error) {
	if e := b.ReserveNodes(32, 64); e != nil {
		return backend.JSONValue{}, nil, e
	}
	ops := make([]string, 0, 16)
	if e := jsonDepthBounded(text); e != nil {
		return backend.JSONValue{}, ops, e
	}
	if opt.JSONStructural {
		s, did, e := cleanZeroWidthBounded(b, text)
		if e != nil {
			return backend.JSONValue{}, ops, e
		}
		if did {
			ops = append(ops, "json.strip-bom-zw")
		}
		text = s
	}
	body := pickEnvelopeBody(text, opt)
	decode := func(s string) (backend.JSONValue, error) {
		data, e := copyBytesBounded(b, s)
		if e != nil {
			return backend.JSONValue{}, e
		}
		return backend.NewJSONView(b, data)
	}
	v, err := decode(body)
	if err == nil {
		return v, ops, nil
	}
	if errors.Is(err, backend.ErrBuildCapacity) {
		return v, ops, err
	}
	if opt.JSONStructural {
		fixed := body
		if fixed == "" {
			fixed = text
		}
		for kind, op := range [4]string{"json.trailing-comma", "json.escape-control", "json.escape-quotes", "json.close-braces"} {
			if kind == 3 && opt.salvageDeclined {
				continue
			}
			s, e := fixJSONBounded(b, fixed, kind)
			if e != nil {
				return v, ops, e
			}
			if s != fixed {
				ops = append(ops, op)
				fixed = s
			}
		}
		v, err = decode(pickEnvelopeBody(fixed, opt))
		if err == nil {
			return v, ops, nil
		}
		if errors.Is(err, backend.ErrBuildCapacity) {
			return v, ops, err
		}
		if cut, ok := salvageCut(text, opt); ok {
			v, inner, e := mainViewBounded(b, cut, opt.enterSalvage())
			if e == nil {
				ops, e = backend.AppendNode(b, ops, "json.truncation-salvage")
				if e != nil {
					return v, ops, e
				}
				for _, op := range inner {
					ops, e = backend.AppendNode(b, ops, op)
					if e != nil {
						return v, ops, e
					}
				}
				return v, ops, nil
			}
			if errors.Is(e, backend.ErrBuildCapacity) {
				return v, ops, e
			}
		}
	}
	return v, ops, err
}
func parseTextMemberBounded(b *backend.BuildBounds, text, wantedID string, opt Options) (Result, error) {
	if e := b.ReserveNodes(3, 1024); e != nil {
		return Result{}, e
	}
	r := Result{Trans: make(map[string]string, 1)}
	if opt.JSONStructural {
		s, _, e := cleanZeroWidthBounded(b, text)
		if e != nil {
			return r, e
		}
		text = s
	}
	text = stripCodeFence(text)
	var parts, rubyLines []string
	lastID := ""
	inGlossary, inRuby := false, false
	for text != "" {
		line, tail, _ := strings.Cut(text, "\n")
		text = tail
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[glossary]") {
			inGlossary, inRuby = true, false
			continue
		}
		if strings.EqualFold(line, "[ruby]") {
			inRuby, inGlossary = true, false
			continue
		}
		if e := b.ReserveNodes(1, 256); e != nil {
			return r, e
		}
		loc, findErr := backend.FindRegexpSubmatch(b, textLineRe, line)
		if findErr != nil {
			return r, findErr
		}
		if loc != nil {
			lastID = line[loc[2]:loc[3]]
			inGlossary, inRuby = false, false
			if lastID == wantedID {
				parts = nil
				var e error
				parts, e = backend.AppendNode(b, parts, line[loc[4]:loc[5]])
				if e != nil {
					return r, e
				}
			}
			continue
		}
		if inGlossary && wantedID == "" {
			if e := b.ReserveNodes(4, 128); e != nil {
				return r, e
			}
			if entry := parseGlossaryLine(line); entry != nil {
				var e error
				r.Glos, e = backend.AppendNode(b, r.Glos, *entry)
				if e != nil {
					return r, e
				}
			}
			continue
		}
		if inRuby && wantedID != "" {
			fields := rubyLineID(line)
			if fields == wantedID {
				var e error
				rubyLines, e = backend.AppendNode(b, rubyLines, line)
				if e != nil {
					return r, e
				}
			}
			continue
		}
		if !inGlossary && !inRuby && !strings.HasPrefix(line, "[*]") && lastID == wantedID && wantedID != "" {
			var e error
			parts, e = backend.AppendNode(b, parts, line)
			if e != nil {
				return r, e
			}
		}
	}
	if wantedID == "" {
		return r, nil
	}
	if parts == nil {
		var e error
		r.Missing, e = backend.AppendNode(b, r.Missing, wantedID)
		return r, e
	}
	target, e := joinFragmentsBounded(b, parts, "\n", limitText(b))
	if e != nil {
		return r, e
	}
	r.Trans[wantedID] = target
	if len(rubyLines) > 0 {
		r.RubyOutput, e = ruby.ParseSectionRubyOutputBounded(b, rubyLines)
	} else {
		rows, err := ruby.ParseInlineMarkersBounded(b, target)
		e = err
		if len(rows) > 0 {
			r.RubyOutput = map[string][]ruby.OutputEntry{wantedID: rows}
		}
	}
	return r, e
}
func rubyLineID(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[") {
		id, _, ok := strings.Cut(line[1:], "]")
		if ok {
			return id
		}
	}
	if id, _, ok := strings.Cut(line, ":"); ok {
		return strings.TrimSpace(id)
	}
	id, _, _ := strings.Cut(line, "|")
	return strings.TrimSpace(id)
}
