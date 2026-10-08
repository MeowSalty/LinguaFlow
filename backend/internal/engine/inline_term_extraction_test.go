package engine

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

type inlineExtractionBackend struct {
	requests []backend.Request
	response string
}

func (b *inlineExtractionBackend) Name() string { return "inline-extraction-test" }
func (b *inlineExtractionBackend) Close() error { return nil }
func (b *inlineExtractionBackend) Translate(_ context.Context, req backend.Request) (*backend.Response, error) {
	b.requests = append(b.requests, req)
	return &backend.Response{Text: b.response}, nil
}

func TestInlineTermExtractionIsScopedToRoundAndKeepsExistingHints(t *testing.T) {
	ctx := context.Background()
	renderer, err := prompt.NewRenderer(templates.EmbeddedPromptTemplate())
	if err != nil {
		t.Fatal(err)
	}
	first := &inlineExtractionBackend{response: `{"translations":{"1":"星云"},"glossary":[{"source":"Nebula","target":"星云","notes":""}]}`}
	second := &inlineExtractionBackend{response: `{"translations":{"1":"星云和阿波罗"},"glossary":[{"source":"Apollo","target":"阿波罗","notes":""}]}`}
	extraction := execution.DefaultInlineTermExtraction()
	extraction.Enabled = true
	extraction.MaxTermsPer1000Words = 8
	extraction.ConflictStrategy = "off"
	eng, err := NewWithOptions(Options{
		Config:    &Config{SourceLang: "en", TargetLang: "zh", Glossary: GlossaryConfig{Enabled: true}},
		Resources: RuntimeResources{Glossary: glossary.NewMemory()},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Rounds: []Round{
			{Backend: first, Renderer: renderer, ResponseMode: "json_strict", BatchSize: 1, Concurrency: 1, FallbackShrink: 1, InlineTermExtraction: &extraction},
			{Backend: second, Renderer: renderer, ResponseMode: "json_strict", BatchSize: 1, Concurrency: 1, FallbackShrink: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	for i, source := range []string{"Nebula", "Nebula and Apollo"} {
		doc := &pipeline.Document{Segments: []pipeline.Segment{{ID: "1", Source: source, Translate: true}}}
		result, err := eng.ExecuteRound(ctx, i, doc)
		if err != nil || result.FailedSegmentCount != 0 || result.UnresolvedCount != 0 {
			t.Fatalf("round %d failed: result=%+v err=%v", i, result, err)
		}
	}
	if len(first.requests) != 1 || len(second.requests) != 1 {
		t.Fatal("inline extraction should not add a separate model request")
	}
	if !strings.Contains(first.requests[0].System, "附加任务 — 术语抽取") || strings.Contains(second.requests[0].System, "附加任务 — 术语抽取") {
		t.Fatal("term extraction instructions leaked between translation rounds")
	}
	if !strings.Contains(second.requests[0].System, "Nebula => 星云") {
		t.Fatal("disabled extraction stopped the next round using existing glossary entries")
	}
	entries, err := eng.glossary.Lookup(ctx, "Nebula and Apollo", "en", "zh")
	if err != nil || len(entries) != 1 || entries[0].Source != "Nebula" {
		t.Fatalf("enabled round failed to add terms or disabled round added terms: %+v, %v", entries, err)
	}
	handler := eng.Rounds()[0].Handler.(*pipeline.TranslateHandler)
	if handler.MaxTermsPer1000Words != 8 || handler.InlineConflictStrategy != "off" {
		t.Fatal("custom round extraction parameters were not applied")
	}
}

func TestInlineTermExtractionCannotOverrideDisabledGlossary(t *testing.T) {
	ctx := context.Background()
	renderer, err := prompt.NewRenderer(templates.EmbeddedPromptTemplate())
	if err != nil {
		t.Fatal(err)
	}
	memory := glossary.NewMemory()
	if _, err := memory.Add(ctx, glossary.Entry{Source: "API", Target: "接口"}); err != nil {
		t.Fatal(err)
	}
	b := &inlineExtractionBackend{response: `{"translations":{"1":"星云接口"},"glossary":[{"source":"Nebula","target":"星云","notes":""}]}`}
	extraction := execution.DefaultInlineTermExtraction()
	extraction.Enabled = true
	eng, err := NewWithOptions(Options{
		Config:    &Config{SourceLang: "en", TargetLang: "zh"},
		Resources: RuntimeResources{Glossary: memory},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Rounds:    []Round{{Backend: b, Renderer: renderer, BatchSize: 1, Concurrency: 1, FallbackShrink: 1, InlineTermExtraction: &extraction}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	doc := &pipeline.Document{Segments: []pipeline.Segment{{ID: "1", Source: "Nebula API", Translate: true}}}
	result, err := eng.ExecuteRound(ctx, 0, doc)
	if err != nil || result.FailedSegmentCount != 0 || len(b.requests) != 1 {
		t.Fatalf("translation failed with glossary disabled: %+v, %v", result, err)
	}
	if strings.Contains(b.requests[0].System, "附加任务 — 术语抽取") || strings.Contains(b.requests[0].System, "API => 接口") {
		t.Fatal("disabled glossary still supplied hints or extraction instructions")
	}
	entries, err := memory.Lookup(ctx, "Nebula API", "en", "zh")
	if err != nil || len(entries) != 1 || entries[0].Source != "API" {
		t.Fatalf("disabled glossary accepted extracted entries: %+v, %v", entries, err)
	}
	if !extraction.Enabled {
		t.Fatal("applying the runtime switch mutated the saved round configuration")
	}
}
