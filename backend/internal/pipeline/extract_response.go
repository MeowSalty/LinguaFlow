package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

type storedExtraction struct {
	entries        []byte
	parsed         int
	repaired       []string
	parseErr       error
	added, skipped int
}

func (h *ExtractHandler) parseExtraction(ctx context.Context, resp *backend.Response, textMode bool, backendName string, logger *slog.Logger) *storedExtraction {
	if resp.Truncated {
		logTruncatedResponse(logger, backendName)
	}
	result := &storedExtraction{}
	result.parseErr = runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
		if int64(len(resp.Text)) > b.OutputLimit() {
			return backend.ErrBuildCapacity
		}
		opt := h.Repair
		opt.Bounds = b
		parsed, repaired, err := repair.ParseBootstrapByMode(resp.Text, textMode, opt, false)
		result.repaired = repaired
		if err != nil {
			return err
		}
		result.parsed = len(parsed)
		if err = backend.ReserveSlice[glossary.Entry](b, int64(len(parsed))); err != nil {
			return err
		}
		entries := make([]glossary.Entry, 0, len(parsed))
		for _, entry := range parsed {
			if utf8.RuneCountInString(entry.Source) >= h.MinSourceLen {
				entries = append(entries, glossary.Entry{Source: entry.Source, Target: entry.Target, Notes: entry.Notes})
			}
		}
		result.entries, err = b.EncodeJSON(entries, b.OutputLimit())
		return err
	})
	if result.parseErr != nil {
		if !storedDispatchDeferred(ctx, result.parseErr) {
			logger.Warn("extract parse failed", "backend", backendName, "err", result.parseErr, "resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200), "repaired", result.repaired)
		}
	} else if len(result.repaired) > 0 {
		logger.Info("extract response repaired", "backend", backendName, "ops", result.repaired)
	}
	return result
}

func (h *ExtractHandler) processStoredResponse(ctx context.Context, _ *Document, idxs []int, resp *backend.Response, _ int, logger *slog.Logger) batchResult {
	var backendName string
	if h.Backend != nil {
		backendName = h.Backend.Name()
	}
	extraction := h.parseExtraction(ctx, resp, prompt.ProtocolFromResponseMode(h.ResponseMode).IsText(), backendName, logger)
	if wait := workDeferred(extraction.parseErr); wait != nil {
		return batchResult{deferred: true, waitCapacity: wait}
	}
	if storedDispatchDeferred(ctx, extraction.parseErr) {
		return batchResult{deferred: true}
	}
	result := batchResult{extraction: extraction}
	if errors.Is(extraction.parseErr, backend.ErrBuildCapacity) {
		result.failedSegments = idxs
	} else if extraction.parseErr != nil {
		result.unresolved = idxs
	}
	return result
}

func (h *ExtractHandler) publishCapturedExtraction(ctx context.Context, store RoundStore, settlement StoredResponseSettlement, extraction *storedExtraction) error {
	if extraction == nil || extraction.parseErr != nil || len(extraction.entries) == 0 {
		return fmt.Errorf("extract: missing parsed response")
	}
	sink, ok := store.(StoredResponseStore)
	if !ok {
		return fmt.Errorf("extract: store cannot settle its capture")
	}
	runtime := ExecutionRuntimeFromContext(ctx)
	header := settlement.Header
	meta := persistence.Metadata{Operation: string(persistence.BusinessPublication), Class: persistence.Reliable, ResourceID: header.ResourceID, RequestID: header.ID}
	_, nop := h.Glossary.(glossary.Nop)
	if nop {
		return runtime.SaveNamed(ctx, meta, func(saveCtx context.Context) error {
			return sink.SettleStoredResponse(saveCtx, nil, settlement)
		})
	}
	processor, ok := h.Glossary.(glossary.ResponseProcessor)
	if !ok {
		return fmt.Errorf("extract: glossary has no atomic response processor")
	}
	domain := store.PublicationDomain()
	glossaryDomain, err := processor.ResponsePublicationDomain(domain)
	if err != nil {
		return err
	}
	if err = responsecontract.ValidatePublication(domain, glossaryDomain); err != nil {
		return err
	}
	if file, ok := h.Glossary.(*glossary.FileGlossary); ok {
		if err = file.BindAllocationOwner(ctx, runtime.Admission); err != nil {
			return err
		}
	}
	in := glossary.ResponseProcess{
		Extract: true, RequestID: header.ID, CaptureEpoch: header.CaptureEpoch, InputDigest: header.ResponseDigest,
		EntriesPayload: extraction.entries, ItemLimit: runtime.responseLimit(), Build: runtime.Run,
		Publish: func(txCtx context.Context, business glossary.ResponseBusiness, prepared *responsecontract.PreparedPublication) error {
			if prepared == nil || prepared.Install == nil {
				return fmt.Errorf("extract: glossary omitted publication preparation")
			}
			out := settlement
			out.GlossaryReceipt = business.Receipt
			if domain.Medium == responsecontract.PublicationMemory {
				out.Install = func() { _ = prepared.InstallOnce() }
			}
			return sink.SettleStoredResponse(txCtx, nil, out)
		},
	}
	var business glossary.ResponseBusiness
	defer business.Release()
	err = runtime.SaveNamed(ctx, meta, func(saveCtx context.Context) error {
		business.Release()
		var err error
		business, err = processor.ProcessResponse(saveCtx, in)
		return err
	})
	if err != nil {
		return err
	}
	var receipt glossary.ResponseReceipt
	if err := json.Unmarshal(business.Receipt, &receipt); err != nil {
		return fmt.Errorf("extract: decode glossary receipt: %w", err)
	}
	extraction.added, extraction.skipped = receipt.Added, receipt.Skipped
	h.logger().Debug("extract batch ok", "request_id", header.ID, "batch_segments", header.MemberCount, "parsed", extraction.parsed, "added", extraction.added, "skipped", extraction.skipped)
	return nil
}

func (h *ExtractHandler) emitStoredExtraction(ctx context.Context, resp *backend.Response, result batchResult) {
	parsed := result.extraction
	if parsed != nil && !errors.Is(parsed.parseErr, backend.ErrBuildCapacity) {
		emitStoredBatchResponse(ctx, resp, parsed.parseErr, parsed.repaired, parsed.skipped > 0)
	}
}
