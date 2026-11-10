package prompt

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"testing"
)

func TestBoundedAllRoundRenderersPreserveTextAndJSON(t *testing.T) {
	check := func(name string, old func() (string, string, error), bounded func(*backend.BuildBounds) (string, string, error)) {
		t.Helper()
		s, u, e := old()
		if e != nil {
			t.Fatal(e)
		}
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		bs, bu, e := bounded(b)
		if e != nil || s != bs || u != bu {
			t.Fatalf("%s old=%q %q got=%q %q err=%v", name, s, u, bs, bu, e)
		}
	}
	for _, p := range []Protocol{ProtocolText, ProtocolJSONStrict} {
		bootstrap, _ := NewBootstrapRenderer(`{{.SourceLang}}/{{.MaxTerms}}`)
		bd := BootstrapData{SourceLang: "ja", TargetLang: "zh", Texts: []string{"<&>海", "wind"}, Existing: []string{"sea"}, Protocol: p}
		check("bootstrap", func() (string, string, error) { return bootstrap.Render(bd) }, func(b *backend.BuildBounds) (string, string, error) { return bootstrap.RenderBounded(b, bd, 1<<20) })
		prune, _ := NewPruneRenderer(`{{.SourceLang}}`)
		pd := PruneData{SourceLang: "ja", TargetLang: "zh", Entries: []PruneEntry{{Source: "海", Target: "sea", Notes: "<&>"}}, Protocol: p}
		check("prune", func() (string, string, error) { return prune.Render(pd) }, func(b *backend.BuildBounds) (string, string, error) { return prune.RenderBounded(b, pd, 1<<20) })
		semantic, _ := NewSemanticQARenderer(`{{.SourceLang}}`)
		sd := SemanticQAData{SourceLang: "ja", TargetLang: "zh", Segments: []SemanticQASegment{{ID: "1", Source: "海", Target: "sea"}}, Protocol: p}
		check("semantic", func() (string, string, error) { return semantic.Render(sd) }, func(b *backend.BuildBounds) (string, string, error) { return semantic.RenderBounded(b, sd, 1<<20) })
		adjudicate, _ := NewAdjudicationRenderer(`{{.SourceLang}}`)
		ad := AdjudicationData{SourceLang: "ja", TargetLang: "zh", Segments: []AdjudicationSegment{{ID: "1", Source: "海", Target: "sea", Issues: []AdjudicationIssue{{Code: "x", Message: "<&>", MatchedText: "sea"}}}}, Protocol: p}
		check("adjudicate", func() (string, string, error) { return adjudicate.Render(ad) }, func(b *backend.BuildBounds) (string, string, error) { return adjudicate.RenderBounded(b, ad, 1<<20) })
		revise, _ := NewReviseRenderer(`{{.SourceLang}}`)
		rd := ReviseData{SourceLang: "ja", TargetLang: "zh", Segments: []ReviseSegment{{ID: "1", Source: "海", Target: "sea"}}, Protocol: p}
		check("revise", func() (string, string, error) { return revise.Render(rd) }, func(b *backend.BuildBounds) (string, string, error) { return revise.RenderBounded(b, rd, 1<<20) })
	}
}
