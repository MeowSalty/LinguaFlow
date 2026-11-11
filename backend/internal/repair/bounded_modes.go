package repair

import (
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func sectionLinesBounded(b *backend.BuildBounds, text, section string) ([]string, error) {
	text = stripCodeFence(text)
	var out []string
	active := false
	for text != "" {
		line, tail, _ := strings.Cut(text, "\n")
		text = tail
		line = strings.TrimSpace(line)
		if strings.EqualFold(line, "["+section+"]") {
			active = true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			active = false
		}
		if active && line != "" {
			var e error
			out, e = backend.AppendNode(b, out, line)
			if e != nil {
				return nil, e
			}
		}
	}
	return out, nil
}
func parseReviseBounded(text string, isTextMode bool, opt Options) ([]prompt.ReviseRevision, map[string][]ruby.OutputEntry, []string, error) {
	b := opt.Bounds
	if isTextMode {
		revisions, recognized, e := prompt.ParseReviseTextRevisionsBounded(b, text)
		if e != nil {
			return nil, nil, nil, e
		}
		if recognized {
			lines, e := sectionLinesBounded(b, text, "ruby")
			if e != nil {
				return nil, nil, nil, e
			}
			var output map[string][]ruby.OutputEntry
			if len(lines) > 0 {
				output, e = ruby.ParseSectionRubyOutputBounded(b, lines)
			}
			return revisions, output, nil, e
		}
	}
	raw, ops, e := TryRepairEnvelope(text, "revisions", opt)
	if e != nil {
		return nil, nil, ops, e
	}
	revisions, e := decodeKeyedBounded(raw, "revisions", opt, prompt.NormalizeReviseRevisions)
	if e != nil {
		return nil, nil, ops, e
	}
	if e = bareArrayNoEntriesError(revisions, ops, "revisions"); e != nil {
		return nil, nil, ops, e
	}
	var output map[string][]ruby.OutputEntry
	if value, ok := raw["ruby_output"]; ok {
		data, e := b.EncodeJSON(value, b.Snapshot().TextLimit)
		if e != nil {
			return nil, nil, ops, e
		}
		output, e = ruby.ParseOutputMapBounded(b, data)
		if e != nil {
			return nil, nil, ops, e
		}
	}
	return revisions, output, ops, nil
}

func bootstrapTextBounded(text string, opt Options) ([]prompt.BootstrapEntry, []string, bool, error) {
	b := opt.Bounds
	if e := b.ReserveNodes(4, 32); e != nil {
		return nil, nil, false, e
	}
	ops := make([]string, 0, 4)
	if opt.JSONStructural {
		v, did, e := cleanZeroWidthBounded(b, text)
		if e != nil {
			return nil, ops, false, e
		}
		text = v
		if did {
			ops = append(ops, "text.strip-bom-zw")
		}
	}
	if v := stripCodeFence(text); v != text {
		text = v
		ops = append(ops, "text.strip-code-fence")
	}
	if e := b.ReserveNodes(1, 1024); e != nil {
		return nil, ops, false, e
	}
	seen := make(map[string]bool)
	var out []prompt.BootstrapEntry
	header, in := false, false
	for text != "" {
		line, tail, _ := strings.Cut(text, "\n")
		text = tail
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[glossary]") {
			header, in = true, true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if header {
				in = false
			}
			continue
		}
		if header && !in {
			continue
		}
		if e := b.ReserveNodes(4, 128); e != nil {
			return nil, ops, header, e
		}
		entry := parseGlossaryLine(line)
		if entry == nil || seen[entry.Source] {
			continue
		}
		if e := b.ReserveNodes(1, 1024); e != nil {
			return nil, ops, header, e
		}
		seen[entry.Source] = true
		var e error
		out, e = backend.AppendNode(b, out, *entry)
		if e != nil {
			return nil, ops, header, e
		}
	}
	return out, ops, header, nil
}
