package service

import (
	"context"
	"encoding/json"
	"fmt"

	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

var _ glossary.ResponseProcessor = (*DatabaseGlossary)(nil)

func (g *DatabaseGlossary) ProcessResponse(ctx context.Context, in glossary.ResponseProcess) (glossary.ResponseBusiness, error) {
	var out glossary.ResponseBusiness
	g.mu.Lock()
	defer g.mu.Unlock()
	var added []glossary.Entry
	// The fixed mask fits the 32KiB response metadata allowance. Entry trees
	// are decoded individually; committed entries move into the dictionary M.
	var addedMask [65536 / 8]byte
	encoded := in.EntriesPayload != nil
	err := workstate.Transaction(ctx, g.client, func(tx *ent.Client) error {
		req, err := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(in.RequestID), workrequest.HasJobWith(job.ProjectIDEQ(g.projectID))).Only(ctx)
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
		if len(req.GlossaryReceipt) > 0 {
			var r glossary.ResponseReceipt
			if err := json.Unmarshal(req.GlossaryReceipt, &r); err != nil || r.Version != 2 || r.RequestID != in.RequestID || r.InputDigest != in.InputDigest {
				return workstate.ErrManifest
			}
			out = glossary.ResponseBusiness{Body: req.CorrectedTargets, Index: req.CorrectedTargetIndex, Receipt: req.GlossaryReceipt}
			return nil
		}
		j, err := tx.Job.Get(ctx, req.JobID)
		if err != nil {
			return err
		}
		if err := executionFormatForResponse(j); err != nil {
			return err
		}
		if j.Status != JobStatusRunning && j.Status != JobStatusPausing && j.Status != JobStatusPending {
			return workstate.ErrStopped
		}
		digest, err := workstate.ExecutionSnapshotDigest(j.ExecutionConfig)
		if err != nil || digest != req.SnapshotDigest {
			return workstate.ErrManifest
		}
		round, err := tx.JobRound.Get(ctx, req.JobRoundID)
		if err != nil {
			return err
		}
		jr, err := tx.JobResource.Get(ctx, round.JobResourceID)
		if err != nil {
			return err
		}
		if err := guardJobResourceSource(ctx, tx, jr); err != nil {
			return err
		}
		if req.ResponseFormatVersion != 1 || req.State != "completed" || req.PromotionState != "response_pending" || req.PromotionCursor != 0 {
			return workstate.ErrManifest
		}
		// Existing rows are projected to identity and target; notes/source bodies are
		// never copied into the receipt or response working set.
		if encoded {
			var a, s int
			a, s, err = absorbResponseEncodedGlossary(ctx, tx, g.projectID, in, &addedMask)
			if err != nil {
				return err
			}
			out, err = glossary.BuildResponseBusinessCounts(ctx, in, a, s)
		} else {
			result, e := absorbResponseGlossary(ctx, tx, g.projectID, in.Entries, in.ItemLimit)
			if e != nil {
				return e
			}
			added = result.Added
			out, err = glossary.BuildResponseBusiness(ctx, in, result)
		}
		if err != nil {
			return err
		}
		return workstate.SaveResponseBusinessTx(ctx, tx, req, out.Body, out.Index, out.Receipt)
	})
	if err != nil && ctx.Err() == nil {
		req, lookup := g.client.WorkRequest.Query().Where(workrequest.IdentityEQ(in.RequestID), workrequest.HasJobWith(job.ProjectIDEQ(g.projectID))).Only(ctx)
		if lookup == nil && len(req.GlossaryReceipt) > 0 {
			var r glossary.ResponseReceipt
			if json.Unmarshal(req.GlossaryReceipt, &r) == nil && r.Version == 2 && r.RequestID == in.RequestID && r.InputDigest == in.InputDigest {
				out = glossary.ResponseBusiness{Body: req.CorrectedTargets, Index: req.CorrectedTargetIndex, Receipt: req.GlossaryReceipt}
				err = nil
			}
		}
	}
	if err != nil {
		return glossary.ResponseBusiness{}, err
	}
	if encoded {
		// Decode after the atomic business commit. Each entry immediately
		// becomes immutable dictionary input, rather than transaction workspace.
		if err := glossary.VisitResponseEntries(context.WithoutCancel(ctx), in, func(i int, e glossary.Entry) error {
			if addedMask[i/8]&(1<<uint(i%8)) == 0 {
				return nil
			}
			input, err := normalizeGlossaryEntryInput(GlossaryEntryInput{Source: e.Source, Target: e.Target, Notes: e.Notes, CaseSensitive: e.CaseSensitive, Forbidden: e.Forbidden, Mandatory: &e.Mandatory})
			if err != nil {
				return err
			}
			g.addCachedEntryLocked(glossary.Entry{Source: input.Source, Target: input.Target, Notes: input.Notes, CaseSensitive: input.CaseSensitive, Forbidden: input.Forbidden, Mandatory: *input.Mandatory})
			return nil
		}); err != nil {
			return glossary.ResponseBusiness{}, err
		}
	} else {
		for _, e := range added {
			g.addCachedEntryLocked(e)
		}
	}
	g.revision++
	clear(g.lookupCache)
	return out, nil
}

func absorbResponseEncodedGlossary(ctx context.Context, tx *ent.Client, project int, in glossary.ResponseProcess, mask *[65536 / 8]byte) (added, skipped int, err error) {
	err = glossary.VisitResponseEntries(ctx, in, func(i int, e glossary.Entry) error {
		input, err := normalizeGlossaryEntryInput(GlossaryEntryInput{Source: e.Source, Target: e.Target, Notes: e.Notes, CaseSensitive: e.CaseSensitive, Forbidden: e.Forbidden, Mandatory: &e.Mandatory})
		if err != nil {
			skipped++
			return nil
		}
		existing, err := requestGlossaryEntryProjected(ctx, tx, project, input, in.ItemLimit)
		if err == nil {
			if existing.Target != input.Target {
				skipped++
				return glossary.ApplyResponseConflict(ctx, in, input.Target, existing.Target)
			}
			return nil
		}
		if !ent.IsNotFound(err) {
			return err
		}
		if _, err = createGlossaryEntry(ctx, tx, project, input); err != nil {
			return err
		}
		added++
		mask[i/8] |= 1 << uint(i%8)
		return nil
	})
	return
}

func executionFormatForResponse(j *ent.Job) error {
	_, err := workstate.ExecutionSnapshotDigest(j.ExecutionConfig)
	return err
}

func absorbResponseGlossary(ctx context.Context, tx *ent.Client, projectID int, entries []glossary.Entry, itemLimit int64) (glossary.AddResult, error) {
	var result glossary.AddResult
	for _, entry := range entries {
		input, err := normalizeGlossaryEntryInput(GlossaryEntryInput{Source: entry.Source, Target: entry.Target, Notes: entry.Notes, Mandatory: &entry.Mandatory})
		if err != nil {
			result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Reason: glossary.SkipReasonEmpty})
			continue
		}
		existing, err := requestGlossaryEntryProjected(ctx, tx, projectID, input, itemLimit)
		if err == nil {
			if existing.Target != input.Target {
				result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Existing: glossary.Entry{Target: existing.Target}, Reason: glossary.SkipReasonExists})
			}
			continue
		}
		if !ent.IsNotFound(err) {
			return result, err
		}
		created, err := createGlossaryEntry(ctx, tx, projectID, input)
		if err != nil {
			return result, err
		}
		result.Added = append(result.Added, glossaryEntryFromRow(created))
	}
	return result, nil
}

func requestGlossaryEntryProjected(ctx context.Context, tx *ent.Client, project int, input GlossaryEntryInput, limit int64) (*ent.GlossaryEntry, error) {
	q := tx.GlossaryEntry.Query().Where(glossaryentry.ProjectIDEQ(project), glossaryentry.SourceKeyEQ(glossarySourceKey(input.Source)), glossaryentry.ForbiddenEQ(input.Forbidden))
	var size []struct {
		ID    int   `json:"id"`
		Bytes int64 `json:"bytes"`
	}
	if err := q.Clone().Select(glossaryentry.FieldID).Aggregate(func(s *sql.Selector) string {
		return sql.As("length(CAST("+s.C(glossaryentry.FieldTarget)+" AS BLOB))", "bytes")
	}).Scan(ctx, &size); err != nil {
		return nil, err
	}
	if len(size) == 0 {
		return nil, &ent.NotFoundError{}
	}
	if len(size) != 1 {
		return nil, workstate.ErrManifest
	}
	if size[0].Bytes > limit {
		return nil, fmt.Errorf("%w: existing glossary target exceeds candidate bound", backend.ErrBuildCapacity)
	}
	return tx.GlossaryEntry.Query().Where(glossaryentry.IDEQ(size[0].ID)).Select(glossaryentry.FieldID, glossaryentry.FieldTarget).Only(ctx)
}
