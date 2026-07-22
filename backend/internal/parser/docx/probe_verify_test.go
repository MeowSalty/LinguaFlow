package docx

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func TestProbe_HyperlinkPreserved(t *testing.T) {
	xmlIn := wrapBody(`<w:p><w:hyperlink r:id="rId1" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:r><w:t>click</w:t></w:r></w:hyperlink></w:p>`)
	_, out := roundTrip(t, xmlIn, func(doc *pipeline.Document) {
		doc.Segments[0].Target = "点击"
	})
	if !strings.Contains(out, "hyperlink") {
		t.Errorf("hyperlink wrapper lost: %s", out)
	}
	if !strings.Contains(out, "rId1") {
		t.Errorf("r:id lost: %s", out)
	}
	if !strings.Contains(out, "点击") {
		t.Errorf("target text lost: %s", out)
	}
}

func TestProbe_HighlightColorPreserved(t *testing.T) {
	xmlIn := wrapBody(para(run(`<w:highlight w:val="red"/>`, "Hi")))
	doc := parseDOCX(t, xmlIn)
	ex, _ := doc.Segments[0].Meta["docx_rpr_extras"].(string)
	if !strings.Contains(ex, `highlight`) || !strings.Contains(ex, `red`) {
		t.Fatalf("extras=%q want highlight red", ex)
	}
	_, out := roundTrip(t, xmlIn, nil)
	if !strings.Contains(out, `highlight`) || !strings.Contains(out, `red`) {
		t.Errorf("highlight color not preserved: %s", out)
	}
}

func TestProbe_RStyleInjectionBlocked(t *testing.T) {
	xmlIn := wrapBody(para(run(`<w:rStyle w:val="x&quot;&gt;&lt;b&gt;evil"/>`, "Styled")))
	doc := parseDOCX(t, xmlIn)
	src := doc.Segments[0].Source
	if strings.Contains(src, "<b>evil") {
		t.Errorf("rStyle injection not blocked: %q", src)
	}
}
