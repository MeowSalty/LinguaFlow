package repair

import (
	"errors"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func ParseReviseMemberBounded(b *backend.BuildBounds, text string, textMode bool, wantedID string, opt Options) (*prompt.ReviseRevision, map[string][]ruby.OutputEntry, error) {
	opt.Bounds = b
	if e := b.ReserveNodes(3, 1024); e != nil {
		return nil, nil, e
	}
	if textMode {
		in, header, content, valid := false, false, false, false
		var revision *prompt.ReviseRevision
		var lines []string
		inRuby := false
		for rest := stripCodeFence(text); rest != ""; {
			line, tail, _ := strings.Cut(rest, "\n")
			rest = tail
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.EqualFold(line, "[revisions]") {
				in, header, inRuby = true, true, false
				continue
			}
			if strings.EqualFold(line, "[ruby]") {
				in, inRuby = false, true
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				in, inRuby = false, false
				continue
			}
			if inRuby && rubyLineID(line) == wantedID {
				var e error
				lines, e = backend.AppendNode(b, lines, line)
				if e != nil {
					return nil, nil, e
				}
				continue
			}
			if !in {
				continue
			}
			content = true
			id, target, ok := strings.Cut(line, "|")
			id, target = strings.TrimSpace(id), strings.TrimSpace(target)
			if !ok || id == "" || target == "" {
				continue
			}
			valid = true
			if id == wantedID && revision == nil {
				if int64(len(target)) > limitText(b) {
					return nil, nil, &backend.BuildCapacityError{Resource: "revision target bytes"}
				}
				if e := b.ReserveNodes(1, 32); e != nil {
					return nil, nil, e
				}
				revision = &prompt.ReviseRevision{ID: id, Target: target}
			}
		}
		if header && (valid || !content) {
			var output map[string][]ruby.OutputEntry
			var e error
			if len(lines) > 0 {
				output, e = ruby.ParseSectionRubyOutputBounded(b, lines)
			}
			return revision, output, e
		}
	}
	var view backend.JSONValue
	var e error
	bare := strings.TrimSpace(stripCodeFence(text))
	if opt.JSONStructural && strings.HasPrefix(bare, "[") {
		data, err := copyBytesBounded(b, bare)
		if err != nil {
			return nil, nil, err
		}
		raw, _, err := backend.FirstJSONRaw(b, data)
		if errors.Is(err, backend.ErrBuildCapacity) {
			return nil, nil, err
		}
		if err == nil {
			view, e = backend.NewJSONView(b, raw)
		}
	}
	if view.Kind() == 0 {
		view, _, e = mainViewBounded(b, text, opt)
	}
	if e != nil && !errors.Is(e, backend.ErrBuildCapacity) && opt.JSONStructural {
		for attempts, i := 0, 0; i < len(text) && attempts < robustScanMaxAttempts; i++ {
			if text[i] != '[' {
				continue
			}
			attempts++
			data, err := copyBytesBounded(b, text[i:])
			if err != nil {
				return nil, nil, err
			}
			raw, _, err := backend.FirstJSONRaw(b, data)
			if err != nil {
				if errors.Is(err, backend.ErrBuildCapacity) {
					return nil, nil, err
				}
				continue
			}
			v, err := backend.NewJSONView(b, raw)
			if err != nil {
				return nil, nil, err
			}
			if v.Kind() == '[' {
				view, e = v, nil
				break
			}
		}
	}
	if e != nil {
		return nil, nil, e
	}
	rows := view
	if view.Kind() == '{' {
		rows, e = mainField(view, "revisions")
		if e != nil {
			return nil, nil, e
		}
	}
	if rows.Kind() != '[' {
		return nil, nil, errors.New("revisions must be an array")
	}
	var revision *prompt.ReviseRevision
	e = rows.EachArray(func(row backend.JSONValue) error {
		if revision != nil || row.Kind() != '{' {
			return nil
		}
		id, e := mainField(row, "id")
		if e != nil {
			return e
		}
		if id.Kind() != '"' {
			return nil
		}
		name, e := id.StringLimited(b.Snapshot().TextLimit)
		if e != nil {
			return e
		}
		name = strings.TrimSpace(name)
		if name != wantedID {
			return nil
		}
		target, e := mainField(row, "target")
		if e != nil {
			return e
		}
		if target.Kind() != '"' {
			return nil
		}
		value, e := target.StringLimited(limitText(b))
		if e != nil {
			return e
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		if e = b.ReserveNodes(1, 32); e != nil {
			return e
		}
		revision = &prompt.ReviseRevision{ID: name, Target: value}
		return nil
	})
	if e != nil {
		return nil, nil, e
	}
	output, e := mainField(view, "ruby_output")
	if e != nil {
		return nil, nil, e
	}
	rubyRows, e := uniqueMainField(output, wantedID)
	if e != nil {
		return nil, nil, e
	}
	if rubyRows.Kind() != 0 {
		entries, e := ruby.ParseOutputEntriesBounded(b, rubyRows.Raw())
		if errors.Is(e, backend.ErrBuildCapacity) {
			return nil, nil, e
		}
		if e == nil {
			return revision, map[string][]ruby.OutputEntry{wantedID: entries}, nil
		}
	}
	return revision, nil, nil
}
