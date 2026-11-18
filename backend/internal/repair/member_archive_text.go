package repair

import (
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func (a *Archive) prepareText(b *backend.BuildBounds, text string, index map[string]int, opt Options) (bool, error) {
	if !a.revise && opt.JSONStructural {
		var err error
		text, _, err = cleanZeroWidthBounded(b, text)
		if err != nil {
			return false, err
		}
	}
	last := -1
	inGlossary, inRuby, inRevision := false, false, false
	header, content, valid := false, false, false
	for rest := stripCodeFence(text); rest != ""; {
		line, tail, _ := strings.Cut(rest, "\n")
		rest = tail
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		a.work.SelectionBytes += int64(len(line))
		if err := b.CheckContext(); err != nil {
			return false, err
		}
		if strings.EqualFold(line, "[ruby]") {
			inRuby, inGlossary, inRevision = true, false, false
			continue
		}
		if a.revise {
			if strings.EqualFold(line, "[revisions]") {
				inRevision, header, inRuby = true, true, false
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				inRevision, inRuby = false, false
				continue
			}
			if inRuby {
				if err := a.appendRubyLine(b, index, line); err != nil {
					return false, err
				}
				continue
			}
			if !inRevision {
				continue
			}
			content = true
			id, target, ok := strings.Cut(line, "|")
			id, target = strings.TrimSpace(id), strings.TrimSpace(target)
			if !ok || id == "" || target == "" {
				continue
			}
			valid = true
			if ordinal, found := index[id]; found && !a.slots[ordinal].found {
				s := &a.slots[ordinal]
				var err error
				s.parts, err = backend.AppendNode(b, s.parts, target)
				if err != nil {
					return false, err
				}
				s.found = true
			}
			continue
		}
		if strings.EqualFold(line, "[glossary]") {
			inGlossary, inRuby = true, false
			continue
		}
		loc, err := backend.FindRegexpSubmatch(b, textLineRe, line)
		if err != nil {
			return false, err
		}
		if loc != nil {
			var found bool
			last, found = index[line[loc[2]:loc[3]]]
			if !found {
				last = -1
			}
			inGlossary, inRuby = false, false
			if last >= 0 {
				s := &a.slots[last]
				s.parts = nil
				s.parts, err = backend.AppendNode(b, s.parts, line[loc[4]:loc[5]])
				if err != nil {
					return false, err
				}
				s.found = true
			}
			continue
		}
		if inGlossary {
			a.glossaryLines, err = backend.AppendNode(b, a.glossaryLines, line)
		} else if inRuby {
			err = a.appendRubyLine(b, index, line)
		} else if last >= 0 && !strings.HasPrefix(line, "[*]") {
			s := &a.slots[last]
			s.parts, err = backend.AppendNode(b, s.parts, line)
		}
		if err != nil {
			return false, err
		}
	}
	return !a.revise || header && (valid || !content), nil
}

func (a *Archive) appendRubyLine(b *backend.BuildBounds, index map[string]int, line string) error {
	if ordinal, found := index[archiveRubyLineID(line)]; found {
		s := &a.slots[ordinal]
		var err error
		s.rubyLines, err = backend.AppendNode(b, s.rubyLines, line)
		return err
	}
	return nil
}

func archiveRubyLineID(line string) string {
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

func (a *Archive) textMember(b *backend.BuildBounds, ordinal int, s archiveSlot) (ArchiveMember, error) {
	m := ArchiveMember{Missing: !s.found}
	if !s.found && !a.revise {
		return m, nil
	}
	if s.found {
		target, err := joinFragmentsBounded(b, s.parts, "\n", limitText(b))
		if err != nil {
			return m, err
		}
		if a.revise {
			if err := backend.ReserveSlice[prompt.ReviseRevision](b, 1); err != nil {
				return m, err
			}
			m.Revision = &prompt.ReviseRevision{ID: a.ids[ordinal], Target: target}
		} else {
			m.Target = target
		}
	}
	if len(s.rubyLines) > 0 {
		output, err := ruby.ParseSectionRubyOutputBounded(b, s.rubyLines)
		if err != nil {
			return m, err
		}
		// Legacy section parsing borrows unquoted fields from the captured body.
		// The output frame must not keep that full body alive after publication.
		err = b.CloneInto(output[a.ids[ordinal]], &m.RubyOutput)
		return m, err
	}
	if !a.revise && s.found {
		var err error
		m.RubyOutput, err = ruby.ParseInlineMarkersBounded(b, m.Target)
		return m, err
	}
	return m, nil
}
