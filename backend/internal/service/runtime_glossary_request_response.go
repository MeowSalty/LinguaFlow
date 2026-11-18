package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

type pendingGlossaryCache struct {
	requestID, digest string
	entries           []glossary.Entry
	head              *glossary.EntrySnapshot
	settlement        glossaryCacheSettlement
}

// An unknown COMMIT keeps its unpublished copy and reversible ownership until
// a later authoritative receipt proves commit or its absence proves rollback.
func (g *DatabaseGlossary) resolvePendingCache(ctx context.Context) error {
	p := g.pendingCache
	if p == nil {
		return nil
	}
	req, err := responseBusinessRequest(ctx, g.client, p.requestID, g.projectID)
	if err != nil {
		return err
	}
	if len(req.GlossaryReceipt) > 0 {
		var receipt glossary.ResponseReceipt
		if !responseReceiptMatches(req, glossary.ResponseProcess{RequestID: p.requestID, InputDigest: p.digest}, &receipt) {
			return workstate.ErrManifest
		}
		if p.settlement.commit != nil {
			if err := p.settlement.commit(); err != nil {
				return err
			}
		}
		g.entries = p.entries
		g.entrySnapshot.Store(p.head)
		g.revision++
		clear(g.lookupCache)
	} else if p.settlement.rollback != nil {
		if err := p.settlement.rollback(); err != nil {
			return err
		}
	}
	g.pendingCache = nil
	return nil
}

func (g *DatabaseGlossary) ProcessResponse(ctx context.Context, in glossary.ResponseProcess) (out glossary.ResponseBusiness, resultErr error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.resolvePendingCache(ctx); err != nil {
		return out, err
	}
	var added []glossary.Entry
	var cachedEntries []glossary.Entry
	var cachedHead *glossary.EntrySnapshot
	var cacheSettlement glossaryCacheSettlement
	installed := false
	defer func() {
		if !installed && cacheSettlement.rollback != nil {
			cachedEntries = nil
			cachedHead = nil
			resultErr = errors.Join(resultErr, cacheSettlement.rollback())
		}
	}()
	// Public decisions survive through member rewrites and cache preparation;
	// each transaction attempt retires its private copy before retrying.
	err := workstate.Transaction(ctx, g.client, func(tx *ent.Client) error {
		// A proved-not-committed attempt can retry. Its private dictionary copy
		// must retire before the next attempt prepares another actual copy.
		if cacheSettlement.rollback != nil {
			cachedEntries = nil
			cachedHead = nil
			if err := cacheSettlement.rollback(); err != nil {
				return err
			}
			cacheSettlement = glossaryCacheSettlement{}
		}
		added = nil
		out.Release()
		req, err := responseBusinessRequest(ctx, tx, in.RequestID, g.projectID)
		if err != nil {
			return err
		}
		if err := workstate.LockJob(ctx, tx, req.JobID); err != nil {
			return err
		}
		req, err = responseBusinessRequest(ctx, tx, in.RequestID, g.projectID)
		if err != nil {
			return err
		}
		if req.ResponseFormatVersion != 2 || req.ResponseDigest != in.InputDigest {
			return workstate.ErrManifest
		}
		if len(req.GlossaryReceipt) > 0 {
			var r glossary.ResponseReceipt
			if !responseReceiptMatches(req, in, &r) {
				return workstate.ErrManifest
			}
			out = glossary.ResponseBusiness{Receipt: req.GlossaryReceipt}
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
		if req.ResponseFormatVersion != 2 || req.State != "completed" || req.PromotionState != "outputs_ready" || req.PromotionCursor != 0 {
			return workstate.ErrManifest
		}
		businessCtx := workstate.WithBusinessTransaction(ctx, tx)
		publicBounds, err := backend.NewBuildBoundsFromContext(businessCtx, 0, int64(len(in.EntriesPayload)))
		if err != nil {
			return err
		}
		defer func() { added = nil; publicBounds.CloseRetired() }()
		result, err := absorbResponseEntries(businessCtx, tx, g.projectID, in, publicBounds)
		if err != nil {
			return err
		}
		added = result.Added
		out, err = glossary.BuildResponseBusiness(businessCtx, in, result)
		if err != nil {
			return err
		}
		cachedEntries, cachedHead, cacheSettlement, err = g.prepareCachedEntries(ctx, added)
		if err != nil {
			return err
		}
		if in.Publish == nil {
			return fmt.Errorf("missing response business publisher")
		}
		return in.Publish(businessCtx, out, nil)
	})
	var transactionErr *workstate.TransactionError
	uncertain := errors.As(err, &transactionErr) && transactionErr.Uncertain()
	if err != nil && (ctx.Err() == nil || errors.As(err, &transactionErr) && transactionErr.Uncertain()) {
		// COMMIT can succeed while its acknowledgement or caller context is lost.
		// Publish the prepared cache only after the durable identity proves it.
		factCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer finish()
		req, lookup := responseBusinessRequest(factCtx, g.client, in.RequestID, g.projectID)
		if lookup == nil && len(req.GlossaryReceipt) > 0 {
			var r glossary.ResponseReceipt
			if responseReceiptMatches(req, in, &r) {
				out.Release()
				out = glossary.ResponseBusiness{Receipt: req.GlossaryReceipt}
				err = nil
			}
		}
	}
	if err != nil {
		if uncertain && cachedEntries != nil {
			if cacheSettlement.park != nil {
				cacheSettlement.park()
			}
			g.pendingCache = &pendingGlossaryCache{requestID: in.RequestID, digest: in.InputDigest, entries: cachedEntries, head: cachedHead, settlement: cacheSettlement}
			installed = true // The pending owner, rather than the visible cache, retains it.
		}
		out.Release()
		return glossary.ResponseBusiness{}, err
	}
	if cachedEntries != nil {
		if cacheSettlement.commit != nil {
			if err := cacheSettlement.commit(); err != nil {
				if cacheSettlement.park != nil {
					cacheSettlement.park()
				}
				g.pendingCache = &pendingGlossaryCache{requestID: in.RequestID, digest: in.InputDigest, entries: cachedEntries, head: cachedHead, settlement: cacheSettlement}
				installed = true
				out.Release()
				return glossary.ResponseBusiness{}, err
			}
		}
		g.entries = cachedEntries
		g.entrySnapshot.Store(cachedHead)
	}
	installed = true
	g.revision++
	clear(g.lookupCache)
	return out, nil
}

func absorbResponseEntries(ctx context.Context, tx *ent.Client, projectID int, in glossary.ResponseProcess, b *backend.BuildBounds) (result glossary.AddResult, err error) {
	err = glossary.VisitResponseEntries(ctx, in, func(_ int, entry glossary.Entry) error {
		input, e := normalizeGlossaryEntryInput(GlossaryEntryInput{Source: entry.Source, Target: entry.Target, Notes: entry.Notes, CaseSensitive: entry.CaseSensitive, Forbidden: entry.Forbidden, Mandatory: &entry.Mandatory})
		if e != nil {
			result.Skipped, e = backend.AppendNode(b, result.Skipped, glossary.SkippedEntry{Reason: glossary.SkipReasonEmpty})
			return e
		}
		existing, e := requestGlossaryEntryProjected(ctx, tx, projectID, input, in.ItemLimit)
		if e == nil {
			if existing.Target == input.Target {
				return nil
			}
			var decision glossary.SkippedEntry
			if e = b.CloneInto(glossary.SkippedEntry{Proposed: glossary.Entry{Target: input.Target}, Existing: glossary.Entry{Target: existing.Target}, Reason: glossary.SkipReasonExists}, &decision); e != nil {
				return e
			}
			result.Skipped, e = backend.AppendNode(b, result.Skipped, decision)
			return e
		}
		if !ent.IsNotFound(e) {
			return e
		}
		if _, e = createGlossaryEntry(ctx, tx, projectID, input); e != nil {
			return e
		}
		var owned glossary.Entry
		if e = b.CloneInto(glossary.Entry{Source: input.Source, Target: input.Target, Notes: input.Notes, CaseSensitive: input.CaseSensitive, Forbidden: input.Forbidden, Mandatory: *input.Mandatory}, &owned); e != nil {
			return e
		}
		result.Added, e = backend.AppendNode(b, result.Added, owned)
		return e
	})
	return result, err
}
func executionFormatForResponse(j *ent.Job) error {
	_, err := workstate.ExecutionSnapshotDigest(j.ExecutionConfig)
	return err
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

// The public transaction reads one small header and its bounded receipt; member
// bodies remain in the member table and are borrowed individually by the sink.
func responseBusinessRequest(ctx context.Context, client *ent.Client, id string, project int) (*ent.WorkRequest, error) {
	q := client.WorkRequest.Query().Where(workrequest.IdentityEQ(id), workrequest.HasJobWith(job.ProjectIDEQ(project)))
	row, err := q.Clone().Where(func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			expression := "COALESCE(LENGTH(CAST(" + s.C(workrequest.FieldGlossaryReceipt) + " AS BLOB)),0)"
			if s.Dialect() == "postgres" {
				expression = "COALESCE(OCTET_LENGTH(CAST(" + s.C(workrequest.FieldGlossaryReceipt) + " AS TEXT)),0)"
			}
			b.WriteString(expression + " <= ").Arg(glossary.ResponseReceiptLimit)
		}))
		expression := "COALESCE(LENGTH(CAST(" + s.C(workrequest.FieldGlossaryReceipt) + " AS BLOB)),0)"
		if s.Dialect() == "postgres" {
			expression = "COALESCE(OCTET_LENGTH(CAST(" + s.C(workrequest.FieldGlossaryReceipt) + " AS TEXT)),0)"
		}
		s.Where(sql.Or(sql.NEQ(s.C(workrequest.FieldPromotionState), "business_ready"), sql.ExprP(expression+" = "+s.C(workrequest.FieldPublicBytes))))
	}).Select(workrequest.FieldID, workrequest.FieldIdentity, workrequest.FieldJobID, workrequest.FieldJobRoundID, workrequest.FieldSnapshotDigest, workrequest.FieldResponseDigest, workrequest.FieldResponseFormatVersion, workrequest.FieldState, workrequest.FieldPromotionState, workrequest.FieldPromotionCursor, workrequest.FieldPublicBytes, workrequest.FieldPublicDigest, workrequest.FieldGlossaryReceipt).Only(ctx)
	if ent.IsNotFound(err) {
		if exists, lookup := q.Exist(ctx); lookup != nil {
			return nil, lookup
		} else if exists {
			return nil, workstate.ErrManifest
		}
	}
	return row, err
}

func responseReceiptMatches(req *ent.WorkRequest, in glossary.ResponseProcess, r *glossary.ResponseReceipt) bool {
	sum := sha256.Sum256(req.GlossaryReceipt)
	if int64(len(req.GlossaryReceipt)) != req.PublicBytes || hex.EncodeToString(sum[:]) != req.PublicDigest {
		return false
	}
	return req.ResponseFormatVersion == 2 && req.ResponseDigest == in.InputDigest && req.State == "completed" && req.PromotionState == "business_ready" && len(req.GlossaryReceipt) > 0 && len(req.GlossaryReceipt) <= glossary.ResponseReceiptLimit && json.Unmarshal(req.GlossaryReceipt, r) == nil && r.Version == glossary.ResponseReceiptVersion && r.RequestID == in.RequestID && r.InputDigest == in.InputDigest && len(r.OutcomeDigest) == 64 && r.Added >= 0 && r.Skipped >= 0
}
