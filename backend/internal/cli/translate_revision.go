package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func resolveCLIRevisionInput(cfg *config.CLIConfig, opts translateOptions, fileCount int) (*config.CLIRevisionInput, error) {
	hasRevise := false
	for _, round := range cfg.Execution.Rounds {
		hasRevise = hasRevise || round.Mode == pipeline.RoundModeRevise
	}
	if !hasRevise {
		if opts.changed["revision-input"] {
			return nil, errors.New("--revision-input requires a revise round")
		}
		return nil, nil
	}
	if !opts.changed["revision-input"] {
		return nil, errors.New("revise requires --revision-input with existing translations and semantic issues")
	}
	if fileCount != 1 {
		return nil, errors.New("--revision-input requires exactly one source file")
	}
	return config.LoadCLIRevisionInput(opts.revisionInput)
}

func revisionIssueCodes(eng *engine.Engine) map[string]bool {
	codes := map[string]bool{}
	for _, round := range eng.Rounds() {
		if handler, ok := round.Handler.(*pipeline.ReviseHandler); ok {
			for _, code := range handler.IssueCodes {
				codes[code] = true
			}
		}
	}
	return codes
}

func applyCLIRevisionInput(doc *pipeline.Document, eng *engine.Engine, input *config.CLIRevisionInput) error {
	codes := revisionIssueCodes(eng)
	if len(codes) == 0 {
		if input != nil {
			return errors.New("revision input requires a revise round")
		}
		return nil
	}
	if input == nil || len(input.Segments) == 0 {
		return errors.New("revise requires --revision-input with reviewed segments")
	}
	for _, entry := range input.Segments {
		if entry.Index == nil || *entry.Index < 0 || *entry.Index >= len(doc.Segments) {
			return errors.New("revision input segment index is outside the source document")
		}
		index := *entry.Index
		segment := &doc.Segments[index]
		if segment.Skip || entry.Source != segment.Source {
			return fmt.Errorf("revision input segment %d does not match the source document; review the current source", index)
		}
		issues := make([]qa.QualityIssue, 0, len(entry.Issues))
		inScope := false
		for _, issue := range entry.Issues {
			inScope = inScope || codes[issue.Code]
			resolved := qa.QualityIssue{SegmentIndex: index, Severity: qa.SeverityWarning, Code: issue.Code, Message: issue.Message, Disposition: qa.DispositionPending}
			if issue.Snippet != "" {
				resolved.Span = &qa.Span{MatchedText: issue.Snippet}
			}
			issues = append(issues, resolved)
		}
		if !inScope {
			return fmt.Errorf("revision input segment %d has no issue in the configured revise scopes", index)
		}
		segment.Target, segment.Status, segment.Issues = entry.Target, "translated", issues
	}
	return nil
}

func executeCLIRevisionRound(ctx context.Context, eng *engine.Engine, roundIndex int, doc *pipeline.Document) error {
	handler := eng.Rounds()[roundIndex].Handler.(*pipeline.ReviseHandler)
	codes := make(map[string]bool, len(handler.IssueCodes))
	for _, code := range handler.IssueCodes {
		codes[code] = true
	}
	// Previous translation rounds narrow Translate for their own pending subset.
	// Revision selects reviewed translations independently of that subset.
	for i := range doc.Segments {
		doc.Segments[i].Translate = !doc.Segments[i].Skip
	}
	var mu sync.Mutex
	accepted := map[int]string{}
	result, err := eng.ExecuteRound(ctx, roundIndex, doc, engine.WithBatchHandler(func(ctx context.Context, batch pipeline.BatchResult) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		for _, segment := range batch.Segments {
			accepted[segment.Index] = segment.TargetText
		}
		return nil
	}))
	if err != nil {
		return fmt.Errorf("cli: revise round %d: %w", roundIndex, err)
	}
	// Apply only confirmed successful segments after all concurrent batches stop;
	// terminal preserve callbacks must never clear pending review issues.
	for _, index := range result.Resolved {
		target, ok := accepted[index]
		if !ok {
			return errors.New("revision finished without an accepted result")
		}
		segment := &doc.Segments[index]
		segment.Target, segment.Status = target, "edited"
		remaining := make([]qa.QualityIssue, 0, len(segment.Issues))
		for _, issue := range segment.Issues {
			if !issue.IsPending() || !codes[issue.Code] {
				remaining = append(remaining, issue)
			}
		}
		segment.Issues = remaining
	}
	return nil
}

func validateCLIRevisionComplete(doc *pipeline.Document, eng *engine.Engine) error {
	codes := revisionIssueCodes(eng)
	for _, segment := range doc.Segments {
		for _, issue := range segment.Issues {
			if issue.IsPending() && codes[issue.Code] {
				return errors.New("revision finished with unresolved reviewed segments; output was not written")
			}
		}
	}
	return nil
}
