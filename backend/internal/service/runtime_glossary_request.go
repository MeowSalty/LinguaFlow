package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type inlineGlossaryReceipt struct {
	Version     int                `json:"version"`
	InputDigest string             `json:"input_digest"`
	Result      glossary.AddResult `json:"result"`
}

var _ glossary.RequestAdder = (*DatabaseGlossary)(nil)

// AddForRequest atomically absorbs one main response and preserves its result.
// A replay returns the original conflict decisions even after later human
// glossary edits, so local translation rewrites remain deterministic.
func (g *DatabaseGlossary) AddForRequest(ctx context.Context, id string, entries ...glossary.Entry) (glossary.AddResult, error) {
	if id == "" {
		return g.Add(ctx, entries...)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return glossary.AddResult{}, err
	}
	hash := sha256.Sum256(encoded)
	digest := hex.EncodeToString(hash[:])
	g.mu.Lock()
	defer g.mu.Unlock()
	var result glossary.AddResult
	var cache []glossary.Entry
	err = workstate.Transaction(ctx, g.client, func(tx *ent.Client) error {
		result = glossary.AddResult{}
		req, err := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id)).Only(ctx)
		if err != nil {
			return err
		}
		if err := workstate.LockJob(ctx, tx, req.JobID); err != nil {
			return err
		}
		req, err = tx.WorkRequest.Get(ctx, req.ID)
		if err != nil {
			return err
		}
		j, err := tx.Job.Get(ctx, req.JobID)
		if err != nil {
			return err
		}
		if j.ProjectID != g.projectID {
			return workstate.ErrManifest
		}
		if len(req.GlossaryReceipt) > 0 {
			result, err = decodeInlineGlossaryReceipt(req.GlossaryReceipt, digest)
			if err != nil {
				return err
			}
			cache, err = loadRuntimeGlossaryEntries(ctx, tx, g.projectID)
			return err
		}
		if req.RetryEpoch != j.RetryEpoch || (j.Status != JobStatusRunning && j.Status != JobStatusPausing && j.Status != JobStatusPending) {
			return workstate.ErrStopped
		}
		if (req.State != "received" && req.State != "completed") || req.Stage != "main" {
			return fmt.Errorf("%w: inline glossary requires a received main response", workstate.ErrManifest)
		}
		round, err := tx.JobRound.Get(ctx, req.JobRoundID)
		if err != nil {
			return err
		}
		if round.JobID != j.ID {
			return workstate.ErrManifest
		}
		jr, err := tx.JobResource.Get(ctx, round.JobResourceID)
		if err != nil {
			return err
		}
		r, err := jr.QueryResource().Only(ctx)
		if err != nil {
			return err
		}
		if r.ID != req.ResourceID {
			return workstate.ErrManifest
		}
		if err := guardJobResourceSource(ctx, tx, jr); err != nil {
			return err
		}
		result, err = absorbRequestGlossary(ctx, tx, g.projectID, entries)
		if err != nil {
			return err
		}
		receipt, err := json.Marshal(inlineGlossaryReceipt{Version: 1, InputDigest: digest, Result: result})
		if err != nil {
			return err
		}
		if err := tx.WorkRequest.UpdateOneID(req.ID).SetGlossaryReceipt(receipt).Exec(ctx); err != nil {
			return err
		}
		cache, err = loadRuntimeGlossaryEntries(ctx, tx, g.projectID)
		return err
	})
	if err != nil && ctx.Err() == nil {
		// A lost COMMIT acknowledgement can be resolved from the same receipt,
		// without replaying additions or changing a stored conflict decision.
		if req, lookupErr := g.client.WorkRequest.Query().Where(workrequest.IdentityEQ(id), workrequest.HasJobWith(job.ProjectIDEQ(g.projectID))).Only(ctx); lookupErr == nil && len(req.GlossaryReceipt) > 0 {
			if recovered, receiptErr := decodeInlineGlossaryReceipt(req.GlossaryReceipt, digest); receiptErr == nil {
				if loaded, loadErr := loadRuntimeGlossaryEntries(ctx, g.client, g.projectID); loadErr == nil {
					result, cache, err = recovered, loaded, nil
				}
			}
		}
	}
	if err != nil {
		return glossary.AddResult{}, err
	}
	g.entries = cache
	g.revision++
	clear(g.lookupCache)
	return result, nil
}

func decodeInlineGlossaryReceipt(payload []byte, digest string) (glossary.AddResult, error) {
	var receipt inlineGlossaryReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		return glossary.AddResult{}, fmt.Errorf("%w: invalid inline glossary receipt", workstate.ErrManifest)
	}
	if receipt.Version != 1 || receipt.InputDigest != digest {
		return glossary.AddResult{}, fmt.Errorf("%w: inline glossary receipt version or input changed", workstate.ErrManifest)
	}
	return receipt.Result, nil
}

func loadRuntimeGlossaryEntries(ctx context.Context, client *ent.Client, projectID int) ([]glossary.Entry, error) {
	rows, err := client.GlossaryEntry.Query().Where(glossaryentry.ProjectIDEQ(projectID)).Order(ent.Asc(glossaryentry.FieldSourceKey), ent.Asc(glossaryentry.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]glossary.Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, glossaryEntryFromRow(row))
	}
	return entries, nil
}

func requestGlossaryEntry(ctx context.Context, client *ent.Client, projectID int, input GlossaryEntryInput) (*ent.GlossaryEntry, error) {
	q := client.GlossaryEntry.Query().Where(glossaryentry.ProjectIDEQ(projectID), glossaryentry.SourceKeyEQ(glossarySourceKey(input.Source)), glossaryentry.ForbiddenEQ(input.Forbidden))
	if input.Forbidden {
		q.Where(glossaryentry.TargetEQ(input.Target))
	}
	return q.Only(ctx)
}

func absorbRequestGlossary(ctx context.Context, tx *ent.Client, projectID int, entries []glossary.Entry) (glossary.AddResult, error) {
	var result glossary.AddResult
	for _, entry := range entries {
		normalized, err := normalizeGlossaryEntryInput(GlossaryEntryInput{Source: entry.Source, Target: entry.Target, CaseSensitive: entry.CaseSensitive, Forbidden: entry.Forbidden, Mandatory: &entry.Mandatory, Notes: entry.Notes})
		if err != nil {
			result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Reason: glossary.SkipReasonEmpty})
			continue
		}
		existing, err := requestGlossaryEntry(ctx, tx, projectID, normalized)
		if err == nil {
			if existing.Target != normalized.Target {
				result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Existing: glossaryEntryFromRow(existing), Reason: glossary.SkipReasonExists})
			}
			continue
		}
		if !ent.IsNotFound(err) {
			return glossary.AddResult{}, err
		}
		// Another job or a manual add may win the unique key after the read.
		// A savepoint keeps PostgreSQL usable after that INSERT conflict.
		if _, err := tx.ExecContext(ctx, "SAVEPOINT inline_glossary_entry"); err != nil {
			return glossary.AddResult{}, err
		}
		created, err := createGlossaryEntry(ctx, tx, projectID, normalized)
		if errors.Is(err, ErrGlossaryEntryExists) {
			if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT inline_glossary_entry"); rollbackErr != nil {
				return glossary.AddResult{}, errors.Join(err, rollbackErr)
			}
			if _, releaseErr := tx.ExecContext(ctx, "RELEASE SAVEPOINT inline_glossary_entry"); releaseErr != nil {
				return glossary.AddResult{}, releaseErr
			}
			existing, err = requestGlossaryEntry(ctx, tx, projectID, normalized)
			if err != nil {
				return glossary.AddResult{}, err
			}
			if existing.Target != normalized.Target {
				result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Existing: glossaryEntryFromRow(existing), Reason: glossary.SkipReasonExists})
			}
			continue
		}
		if err != nil {
			return glossary.AddResult{}, err
		}
		if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT inline_glossary_entry"); err != nil {
			return glossary.AddResult{}, err
		}
		result.Added = append(result.Added, glossaryEntryFromRow(created))
	}
	return result, nil
}
