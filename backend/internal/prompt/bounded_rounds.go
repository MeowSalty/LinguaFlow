package prompt

import (
	"strconv"
	"text/template"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func renderRoundBounded(b *backend.BuildBounds, t *template.Template, d, env any, text bool, maxBytes int64, claim func(backend.PromptMeasure) error, build func(*textOutput)) (string, string, error) {
	if text {
		return renderPairBounded(b, t, d, maxBytes, claim, build)
	}
	return renderPairBounded(b, t, d, maxBytes, claim, func(o *textOutput) { o.err = b.WriteJSON(o.w, env) })
}
func languages(o *textOutput, source, target string) {
	o.put("source_lang: ")
	o.put(source)
	o.put("\ntarget_lang: ")
	o.put(target)
	o.put("\n")
}

func (r *BootstrapRenderer) RenderBounded(b *backend.BuildBounds, d BootstrapData, maxBytes int64) (string, string, error) {
	return r.RenderWithCreditsBounded(b, d, maxBytes, nil)
}
func (r *BootstrapRenderer) RenderWithCreditsBounded(b *backend.BuildBounds, d BootstrapData, maxBytes int64, claim func(backend.PromptMeasure) error) (string, string, error) {
	if e := b.CloneInto(d, &d); e != nil {
		return "", "", e
	}
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	if d.MaxTerms < 1 {
		d.MaxTerms = 20
	}
	return renderRoundBounded(b, r.system, d, bootstrapEnvelope{"extract_terms", d.SourceLang, d.TargetLang, d.Existing, d.Texts}, d.Protocol.IsText(), maxBytes, claim, func(o *textOutput) {
		languages(o, d.SourceLang, d.TargetLang)
		o.put("max_terms: ")
		o.put(strconv.Itoa(d.MaxTerms))
		o.put("\n")
		if len(d.Existing) > 0 {
			o.put("\n[existing]\n")
			for _, v := range d.Existing {
				o.put(v)
				o.put("\n")
			}
		}
		o.put("\n[texts]\n")
		for _, v := range d.Texts {
			o.put("---\n")
			o.put(v)
			o.put("\n")
		}
	})
}
func (r *PruneRenderer) RenderBounded(b *backend.BuildBounds, d PruneData, maxBytes int64) (string, string, error) {
	return r.RenderWithCreditsBounded(b, d, maxBytes, nil)
}
func (r *PruneRenderer) RenderWithCreditsBounded(b *backend.BuildBounds, d PruneData, maxBytes int64, claim func(backend.PromptMeasure) error) (string, string, error) {
	if e := b.CloneInto(d, &d); e != nil {
		return "", "", e
	}
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	entries := d.Entries
	if entries == nil {
		entries = []PruneEntry{}
	}
	return renderRoundBounded(b, r.system, d, pruneEnvelope{"refine_glossary", d.SourceLang, d.TargetLang, entries}, d.Protocol.IsText(), maxBytes, claim, func(o *textOutput) {
		languages(o, d.SourceLang, d.TargetLang)
		o.put("\n[entries]\n")
		for _, v := range d.Entries {
			o.put(v.Source)
			o.put(" | ")
			o.put(v.Target)
			if v.Notes != "" {
				o.put(" | ")
				o.put(v.Notes)
			}
			o.put("\n")
		}
	})
}
func (r *SemanticQARenderer) RenderBounded(b *backend.BuildBounds, d SemanticQAData, maxBytes int64) (string, string, error) {
	return r.RenderWithCreditsBounded(b, d, maxBytes, nil)
}
func (r *SemanticQARenderer) RenderWithCreditsBounded(b *backend.BuildBounds, d SemanticQAData, maxBytes int64, claim func(backend.PromptMeasure) error) (string, string, error) {
	if e := b.CloneInto(d, &d); e != nil {
		return "", "", e
	}
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	return renderRoundBounded(b, r.system, d, semanticQAEnvelope{"semantic_quality_scan", d.SourceLang, d.TargetLang, d.Segments}, d.Protocol.IsText(), maxBytes, claim, func(o *textOutput) {
		languages(o, d.SourceLang, d.TargetLang)
		for _, v := range d.Segments {
			o.put("\n[segment] id=")
			o.put(v.ID)
			o.put("\nsource: ")
			o.put(v.Source)
			o.put("\ntarget: ")
			o.put(v.Target)
			o.put("\n")
		}
	})
}
func (r *AdjudicationRenderer) RenderBounded(b *backend.BuildBounds, d AdjudicationData, maxBytes int64) (string, string, error) {
	return r.RenderWithCreditsBounded(b, d, maxBytes, nil)
}
func (r *AdjudicationRenderer) RenderWithCreditsBounded(b *backend.BuildBounds, d AdjudicationData, maxBytes int64, claim func(backend.PromptMeasure) error) (string, string, error) {
	if e := b.CloneInto(d, &d); e != nil {
		return "", "", e
	}
	if d.Protocol == "" {
		d.Protocol = ProtocolJSONStrict
	}
	return renderRoundBounded(b, r.system, d, adjudicationEnvelope{"adjudicate_quality_issues", d.SourceLang, d.TargetLang, d.Segments}, d.Protocol.IsText(), maxBytes, claim, func(o *textOutput) {
		languages(o, d.SourceLang, d.TargetLang)
		for _, v := range d.Segments {
			o.put("\n[segment] id=")
			o.put(v.ID)
			o.put("\nsource: ")
			o.put(v.Source)
			o.put("\ntarget: ")
			o.put(v.Target)
			o.put("\nissues:\n")
			for _, i := range v.Issues {
				o.put("- ")
				o.put(i.Code)
				o.put(" | ")
				o.put(i.MatchedText)
				o.put(": ")
				o.put(i.Message)
				o.put("\n")
			}
		}
	})
}
