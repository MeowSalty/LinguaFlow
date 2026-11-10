package repair

import (
	"errors"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func init() {
	backend.RegisterFixedRegexp(textLineRe)
	backend.RegisterFixedRegexp(placeholderVariantRE)
}

func joinFragmentsBounded(b *backend.BuildBounds, parts []string, separator string, maxBytes int64) (string, error) {
	n := int64(0)
	for i, p := range parts {
		if i > 0 {
			if int64(len(separator)) > maxBytes-n {
				return "", &backend.BuildCapacityError{Resource: "joined target bytes"}
			}
			n += int64(len(separator))
		}
		if int64(len(p)) > maxBytes-n {
			return "", &backend.BuildCapacityError{Resource: "joined target bytes"}
		}
		n += int64(len(p))
	}
	w, e := backend.NewFixedBoundedWriter(b, n)
	if e != nil {
		return "", e
	}
	for i, p := range parts {
		if i > 0 {
			_, _ = w.WriteString(separator)
		}
		_, _ = w.WriteString(p)
	}
	if e = b.ReserveText(n, n); e != nil {
		return "", e
	}
	return string(w.Bytes()), nil
}
func tryRepairTextBounded(text string, wantIDs []string, opt Options) Result {
	b := opt.Bounds
	if e := b.ReserveNodes(4, 1024); e != nil {
		return Result{Fatal: true, ParseErr: e}
	}
	repaired := make([]string, 0, 16)
	if opt.JSONStructural {
		v, did, e := cleanZeroWidthBounded(b, text)
		if e != nil {
			return Result{Fatal: true, ParseErr: e}
		}
		if did {
			repaired = append(repaired, "text.strip-bom-zw")
		}
		text = v
	}
	if v := stripCodeFence(text); v != text {
		text = v
		repaired = append(repaired, "text.strip-code-fence")
	}
	fragments := make(map[string][]string)
	var glos []prompt.BootstrapEntry
	var rubyLines []string
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
			return Result{Fatal: true, ParseErr: e}
		}
		loc, findErr := backend.FindRegexpSubmatch(b, textLineRe, line)
		if findErr != nil {
			return Result{Fatal: true, ParseErr: findErr}
		}
		if loc != nil {
			inGlossary, inRuby = false, false
			lastID = line[loc[2]:loc[3]]
			if e := b.ReserveNodes(1, 1024); e != nil {
				return Result{Fatal: true, ParseErr: e}
			}
			parts, e := backend.AppendNode(b, []string(nil), line[loc[4]:loc[5]])
			if e != nil {
				return Result{Fatal: true, ParseErr: e}
			}
			fragments[lastID] = parts
			continue
		}
		if inGlossary {
			if e := b.ReserveNodes(4, 128); e != nil {
				return Result{Fatal: true, ParseErr: e}
			}
			if entry := parseGlossaryLine(line); entry != nil {
				var e error
				glos, e = backend.AppendNode(b, glos, *entry)
				if e != nil {
					return Result{Fatal: true, ParseErr: e}
				}
			}
			continue
		}
		if inRuby {
			var e error
			rubyLines, e = backend.AppendNode(b, rubyLines, line)
			if e != nil {
				return Result{Fatal: true, ParseErr: e}
			}
			continue
		}
		if strings.HasPrefix(line, "[*]") {
			continue
		}
		if lastID != "" {
			parts, e := backend.AppendNode(b, fragments[lastID], line)
			if e != nil {
				return Result{Fatal: true, ParseErr: e}
			}
			fragments[lastID] = parts
		}
	}
	if len(fragments) == 0 {
		return Result{Fatal: true, Repaired: repaired, ParseErr: errors.New("no translations found in text response")}
	}
	if e := b.ReserveNodes(int64(len(fragments))+1, 1024); e != nil {
		return Result{Fatal: true, ParseErr: e}
	}
	trans := make(map[string]string, len(fragments))
	for id, parts := range fragments {
		value, e := joinFragmentsBounded(b, parts, "\n", limitText(b))
		if e != nil {
			return Result{Fatal: true, ParseErr: e}
		}
		trans[id] = value
	}
	var output map[string][]ruby.OutputEntry
	if len(rubyLines) > 0 {
		var e error
		output, e = ruby.ParseSectionRubyOutputBounded(b, rubyLines)
		if e != nil {
			return Result{Fatal: true, ParseErr: e}
		}
	} else {
		if e := b.ReserveNodes(int64(len(trans))+1, 1024); e != nil {
			return Result{Fatal: true, ParseErr: e}
		}
		for id, value := range trans {
			rows, e := ruby.ParseInlineMarkersBounded(b, value)
			if e != nil {
				return Result{Fatal: true, ParseErr: e}
			}
			if len(rows) > 0 {
				if output == nil {
					output = make(map[string][]ruby.OutputEntry, len(trans))
				}
				output[id] = rows
			}
		}
	}
	if e := b.ReserveNodes(int64(len(wantIDs)), 16); e != nil {
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

func NormalizePlaceholdersBounded(b *backend.BuildBounds, text string, knownKeys map[string]string) (string, []string, error) {
	if len(knownKeys) == 0 {
		return text, nil, nil
	}
	if e := b.ReserveNodes(1, 1024); e != nil {
		return "", nil, e
	}
	seen := make(map[string]bool)
	var normalized []string
	w := backend.NewBoundedWriter(b, b.Snapshot().TextLimit)
	from := 0
	for from < len(text) {
		if e := b.ReserveNodes(1, 256); e != nil {
			return "", nil, e
		}
		loc, findErr := backend.FindRegexpSubmatch(b, placeholderVariantRE, text[from:])
		if findErr != nil {
			return "", nil, findErr
		}
		if loc == nil {
			break
		}
		start, end := from+loc[0], from+loc[1]
		if _, e := w.WriteString(text[from:start]); e != nil {
			return "", nil, e
		}
		match := text[start:end]
		digits := text[from+loc[2] : from+loc[3]]
		value := match
		if len(digits) <= 6 {
			var key [13]byte
			copy(key[:], "__LF_000000__")
			copy(key[11-len(digits):11], digits)
			if e := b.ReserveText(13, 13); e != nil {
				return "", nil, e
			}
			std := string(key[:])
			if _, ok := knownKeys[std]; ok {
				value = std
				if match != std && !seen[std] {
					if e := b.ReserveNodes(1, 1024); e != nil {
						return "", nil, e
					}
					seen[std] = true
					var e error
					normalized, e = backend.AppendNode(b, normalized, std)
					if e != nil {
						return "", nil, e
					}
				}
			}
		}
		if _, e := w.WriteString(value); e != nil {
			return "", nil, e
		}
		from = end
	}
	if _, e := w.WriteString(text[from:]); e != nil {
		return "", nil, e
	}
	if e := b.ReserveText(int64(len(w.Bytes())), int64(len(w.Bytes()))); e != nil {
		return "", nil, e
	}
	return string(w.Bytes()), normalized, nil
}
