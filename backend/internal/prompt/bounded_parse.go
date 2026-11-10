package prompt

import (
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func splitPipesBounded(b *backend.BuildBounds, line string) ([]string, error) {
	count := strings.Count(line, "|") + 1
	if e := b.ReserveNodes(int64(count), 16); e != nil {
		return nil, e
	}
	return strings.Split(line, "|"), nil
}
func joinTrimmedBounded(b *backend.BuildBounds, parts []string) (string, error) {
	return buildTextBounded(b, b.OutputLimit(), func(o *textOutput) {
		for i, p := range parts {
			if i > 0 {
				o.put(" | ")
			}
			o.put(strings.TrimSpace(p))
		}
	})
}
func ParseReviseTextRevisionsBounded(b *backend.BuildBounds, text string) ([]ReviseRevision, bool, error) {
	text = StripCodeFence(text)
	in, header, content := false, false, false
	var out []ReviseRevision
	for text != "" {
		line, tail, _ := strings.Cut(text, "\n")
		text = tail
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[revisions]") {
			in, header = true, true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if header {
				in = false
			}
			continue
		}
		if !header || !in {
			continue
		}
		content = true
		sep := strings.IndexByte(line, '|')
		if sep < 0 {
			continue
		}
		id, target := strings.TrimSpace(line[:sep]), strings.TrimSpace(line[sep+1:])
		if id == "" || target == "" {
			continue
		}
		if int64(len(target)) > b.OutputLimit() {
			return out, false, &backend.BuildCapacityError{Resource: "revision target bytes"}
		}
		var e error
		out, e = backend.AppendNode(b, out, ReviseRevision{id, target})
		if e != nil {
			return out, false, e
		}
	}
	if e := b.ReserveNodes(int64(len(out))+1, 1024); e != nil {
		return out, false, e
	}
	return NormalizeReviseRevisions(out), header && (len(out) > 0 || !content), nil
}
func ParseSemanticQATextIssuesBounded(b *backend.BuildBounds, text string) ([]SemanticQAIssue, bool, error) {
	text = StripCodeFence(text)
	in, header, content := false, false, false
	var out []SemanticQAIssue
	for text != "" {
		line, tail, _ := strings.Cut(text, "\n")
		text = tail
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[issues]") {
			in, header = true, true
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
		if header {
			content = true
		}
		parts, e := splitPipesBounded(b, line)
		if e != nil {
			return out, false, e
		}
		if len(parts) < 2 {
			continue
		}
		issue := SemanticQAIssue{ID: strings.TrimSpace(parts[0]), Code: strings.TrimSpace(parts[1])}
		if issue.ID == "" || !IsSemanticQACode(issue.Code) {
			continue
		}
		if len(parts) == 3 {
			issue.Message = strings.TrimSpace(parts[2])
		} else if len(parts) > 3 {
			issue.Snippet = strings.TrimSpace(parts[2])
			issue.Message, e = joinTrimmedBounded(b, parts[3:])
			if e != nil {
				return out, false, e
			}
		}
		out, e = backend.AppendNode(b, out, issue)
		if e != nil {
			return out, false, e
		}
	}
	return out, len(out) > 0 || (header && !content), nil
}
func ParseAdjudicationTextVerdictsBounded(b *backend.BuildBounds, text string) ([]AdjudicationVerdict, bool, error) {
	text = StripCodeFence(text)
	in, header, content := false, false, false
	var out []AdjudicationVerdict
	for text != "" {
		line, tail, _ := strings.Cut(text, "\n")
		text = tail
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[verdicts]") {
			in, header = true, true
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
		if header {
			content = true
		}
		parts, e := splitPipesBounded(b, line)
		if e != nil {
			return out, false, e
		}
		if len(parts) < 3 {
			continue
		}
		v := AdjudicationVerdict{ID: strings.TrimSpace(parts[0]), IssueCode: strings.TrimSpace(parts[1])}
		reason := 3
		v.Verdict = strings.TrimSpace(parts[2])
		if len(parts) >= 4 && v.Verdict != "real" && v.Verdict != "false_positive" {
			v.MatchedText = v.Verdict
			v.Verdict = strings.TrimSpace(parts[3])
			reason = 4
		}
		if v.ID == "" || v.IssueCode == "" || (v.Verdict != "real" && v.Verdict != "false_positive") {
			continue
		}
		if len(parts) > reason {
			v.Reason, e = joinTrimmedBounded(b, parts[reason:])
			if e != nil {
				return out, false, e
			}
		}
		out, e = backend.AppendNode(b, out, v)
		if e != nil {
			return out, false, e
		}
	}
	return out, len(out) > 0 || (header && !content), nil
}
