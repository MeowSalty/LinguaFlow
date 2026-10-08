package engine

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

type extractSkipReporter struct {
	progress.Nop
	stages []string
}

func (r *extractSkipReporter) StageStart(name string, _ int) { r.stages = append(r.stages, name) }

func TestDisabledGlossarySkipsExtractBeforePreparingDocument(t *testing.T) {
	extractRenderer, err := prompt.NewBootstrapRenderer(templates.EmbeddedBootstrapTemplate())
	if err != nil {
		t.Fatal(err)
	}
	translateRenderer, err := prompt.NewRenderer(templates.EmbeddedPromptTemplate())
	if err != nil {
		t.Fatal(err)
	}
	extractor := &inlineExtractionBackend{response: `{"glossary":[]}`}
	translator := &inlineExtractionBackend{response: `{"translations":{"1":"你好"}}`}
	reporter := &extractSkipReporter{}
	eng, err := NewWithOptions(Options{
		Config: &Config{SourceLang: "en", TargetLang: "zh"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Reporter: reporter,
		Rounds: []Round{
			{Mode: pipeline.RoundModeExtract, Backend: extractor, ExtractRenderer: extractRenderer, Concurrency: 1},
			{Mode: pipeline.RoundModeTranslate, Backend: translator, Renderer: translateRenderer, BatchSize: 1, Concurrency: 1, FallbackShrink: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	doc := &pipeline.Document{Segments: []pipeline.Segment{{ID: "1", Source: "hello", Translate: true}}}
	want := &pipeline.Document{Segments: []pipeline.Segment{{ID: "1", Source: "hello", Translate: true}}}
	result, err := eng.ExecuteRound(context.Background(), 0, doc)
	if err != nil || !result.RoundSkipped || result.SkipReason != "glossary_disabled" {
		t.Fatalf("skipped result=%+v err=%v", result, err)
	}
	if !reflect.DeepEqual(doc, want) || len(reporter.stages) != 0 || len(extractor.requests) != 0 {
		t.Fatalf("skipped extraction changed document or started work: doc=%+v stages=%v requests=%d", doc, reporter.stages, len(extractor.requests))
	}
	result, err = eng.ExecuteRound(context.Background(), 1, doc)
	if err != nil || result.RoundSkipped || result.UnresolvedCount != 0 || len(translator.requests) != 1 {
		t.Fatalf("following translation failed: result=%+v requests=%d err=%v", result, len(translator.requests), err)
	}
	if !reflect.DeepEqual(reporter.stages, []string{pipeline.RoundModeTranslate}) {
		t.Fatalf("stages=%v", reporter.stages)
	}
}
