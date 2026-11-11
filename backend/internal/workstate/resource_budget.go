package workstate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// InventorySource reads scalar counts and byte lengths. Neither the driver nor
// the caller materializes saved bodies or a map of historical identities.
type InventorySource struct {
	DB interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}
	Driver    string
	ItemBytes int64
	ActorID   int
}

type inventoryRow struct {
	rows *sql.Rows
	err  error
}

func (r inventoryRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	return r.rows.Scan(dest...)
}
func (s InventorySource) row(ctx context.Context, q string, args ...any) inventoryRow {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	return inventoryRow{rows, err}
}

func StorageIdentity(jobID int) string { return fmt.Sprintf("job:%d:storage", jobID) }
func RuntimeIdentity(jobID int) string { return fmt.Sprintf("job:%d:runtime", jobID) }

const inventoryActor = "CASE WHEN creator.id IS NOT NULL THEN creator.id WHEN p.owner_org_id IS NULL AND owner.id IS NOT NULL THEN owner.id ELSE 0 END"

func (s InventorySource) Actor(ctx context.Context, jobID int) (int, error) {
	var actor int
	err := s.row(ctx, s.bind("SELECT "+inventoryActor+" FROM jobs j JOIN projects p ON p.id=j.project_id LEFT JOIN users creator ON creator.id=j.user_created_jobs LEFT JOIN users owner ON owner.id=p.owner_user_id WHERE j.id=?"), jobID).Scan(&actor)
	return actor, err
}

func (s InventorySource) Scan(ctx context.Context, emit func(resourcebudget.Record) error) error {
	for after := 0; ; {
		query := "SELECT j.id, " + inventoryActor + " FROM jobs j JOIN projects p ON p.id=j.project_id LEFT JOIN users creator ON creator.id=j.user_created_jobs LEFT JOIN users owner ON owner.id=p.owner_user_id WHERE j.id > ?"
		args := []any{after}
		if s.ActorID > 0 {
			query += " AND (" + inventoryActor + ") = ?"
			args = append(args, s.ActorID)
		}
		query += " ORDER BY j.id LIMIT 16"
		rows, err := s.DB.QueryContext(ctx, s.bind(query), args...)
		if err != nil {
			return err
		}
		type jobActor struct{ job, actor int }
		page := make([]jobActor, 0, 16)
		for rows.Next() {
			var j jobActor
			if err = rows.Scan(&j.job, &j.actor); err != nil {
				break
			}
			page = append(page, j)
		}
		err = errorsJoinRows(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, j := range page {
			v, storage, err := s.Job(ctx, j.job)
			if err != nil {
				return fmt.Errorf("job %d inventory: %w", j.job, err)
			}
			scope := resourcebudget.Scope{ActorID: j.actor, JobID: j.job}
			if err = emit(resourcebudget.Record{Scope: scope, Identity: RuntimeIdentity(j.job), Usage: v}); err != nil {
				return err
			}
			if err = emit(resourcebudget.Record{Scope: scope, Identity: StorageIdentity(j.job), Usage: resourcebudget.Vector{LogicalStorageBytes: storage}}); err != nil {
				return err
			}
			after = j.job
		}
	}
}

func errorsJoinRows(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s InventorySource) bind(q string) string {
	if s.Driver != "postgres" {
		return q
	}
	for i := 1; strings.Contains(q, "?"); i++ {
		q = strings.Replace(q, "?", fmt.Sprintf("$%d", i), 1)
	}
	return q
}

func (s InventorySource) length(column string) string {
	if s.Driver == "postgres" {
		if storageBinaryColumn(column) {
			return "COALESCE(OCTET_LENGTH(" + column + "),0)"
		}
		return "COALESCE(OCTET_LENGTH(CAST(" + column + " AS TEXT)),0)"
	}
	return "COALESCE(LENGTH(CAST(" + column + " AS BLOB)),0)"
}

func storageBinaryColumn(column string) bool {
	if i := strings.LastIndexByte(column, '.'); i >= 0 {
		column = column[i+1:]
	}
	switch column {
	case "freeze_payload", "response_payload", "corrected_targets", "corrected_target_index", "promotion_status", "pipeline_header":
		return true
	default:
		return false
	}
}

// Job counts all paused/failed backing, independent of job status and epoch.
func (s InventorySource) Job(ctx context.Context, jobID int) (v resourcebudget.Vector, storage int64, err error) {
	if s.DB == nil || s.ItemBytes <= 0 {
		return v, 0, fmt.Errorf("invalid inventory source")
	}
	segments, candidates, growth, handoff, inventoryErr := s.Runtime(ctx, jobID)
	err = inventoryErr
	if err != nil {
		return
	}
	v.UnconfirmedMembers = segments
	for _, n := range []int64{candidates, growth, handoff} {
		if err = addInventory(&v.PayloadBytes, n); err != nil {
			return
		}
	}
	metadata, metadataErr := s.Metadata(ctx, jobID)
	if metadataErr != nil {
		err = metadataErr
		return
	}
	if err = addInventory(&v.PayloadBytes, metadata); err != nil {
		return
	}
	requestFields := []string{"freeze_payload", "response_payload", "corrected_targets", "corrected_target_index", "glossary_receipt", "promotion_members", "promotion_status", "members", "debits", "segment_ids"}
	var lengths []string
	for _, c := range requestFields {
		lengths = append(lengths, s.length(c))
	}
	var requestStorage int64
	err = s.row(ctx, s.bind("SELECT COALESCE(SUM("+strings.Join(lengths, "+")+"),0) FROM work_requests WHERE job_id=?"), jobID).Scan(&requestStorage)
	if err != nil {
		return
	}
	var candidateStorage int64
	err = s.row(ctx, s.bind("SELECT COALESCE(SUM("+s.length("c.payload")+"+"+s.length("c.pipeline_header")+"+"+s.length("c.baseline_target")+"),0) FROM work_candidates c JOIN work_items w ON w.id=c.work_item_id WHERE w.job_id=?"), jobID).Scan(&candidateStorage)
	if err != nil {
		return
	}
	if err = addInventory(&storage, requestStorage); err != nil {
		return
	}
	if err = addInventory(&storage, candidateStorage); err != nil {
		return
	}
	return
}

// Metadata is separate from the Job's H/J ledger. Reliable frozen/response
// backings and unresolved attempt owners retain their member-table payload;
// terminal audit descriptors without such backing consume only storage.
func (s InventorySource) Metadata(ctx context.Context, jobID int) (int64, error) {
	var lengths []string
	for _, field := range requestMetadataFields {
		if field != "glossary_receipt" {
			lengths = append(lengths, s.length(field))
		}
	}
	var n int64
	err := s.row(ctx, s.bind("SELECT COALESCE(SUM("+joinLengths(lengths)+"),0) FROM work_requests WHERE job_id=? AND "+s.metadataBackingPredicate()), jobID).Scan(&n)
	return n, err
}

func (s InventorySource) RequestMetadata(ctx context.Context, requestID string) (int64, error) {
	_, n, err := s.RequestMetadataFacts(ctx, requestID)
	return n, err
}

func (s InventorySource) RequestMetadataFacts(ctx context.Context, requestID string) (members int, n int64, err error) {
	var lengths []string
	for _, field := range requestMetadataFields {
		if field != "glossary_receipt" {
			lengths = append(lengths, s.length(field))
		}
	}
	count := "COALESCE(json_array_length(segment_ids),0)"
	if s.Driver == "postgres" {
		count = "COALESCE(jsonb_array_length(segment_ids),0)"
	}
	err = s.row(ctx, s.bind("SELECT "+count+",CASE WHEN "+s.metadataBackingPredicate()+" THEN "+joinLengths(lengths)+" ELSE 0 END FROM work_requests WHERE identity=?"), requestID).Scan(&members, &n)
	return
}

func (s InventorySource) metadataBackingPredicate() string {
	backing := s.length("freeze_payload") + "+" + s.length("response_payload") + "+" + s.length("corrected_targets")
	return "((" + backing + ")>0 OR (promotion_state<>'cancelled' AND state IN ('reserved','sent','received','unknown') AND EXISTS (SELECT 1 FROM jobs WHERE id=work_requests.job_id AND status NOT IN ('completed','cancelled'))))"
}

// Runtime projects retained inventory after every writer and provider has
// joined. Stored freeze/raw/corrected backing counts in every request state.
func (s InventorySource) Runtime(ctx context.Context, jobID int) (segments, candidates, growth, handoff int64, err error) {
	if s.DB == nil || s.ItemBytes <= 0 {
		err = fmt.Errorf("invalid inventory source")
		return
	}
	err = s.row(ctx, s.bind("SELECT COUNT(*) FROM work_items WHERE job_id=? AND (member_owned OR state IN ('candidate','response_pending'))"), jobID).Scan(&segments)
	if err != nil {
		return
	}
	p := s.length("freeze_payload") + "+" + s.length("response_payload") + "+" + s.length("corrected_targets")
	err = s.row(ctx, s.bind("SELECT COALESCE(SUM(CASE WHEN ("+p+")>0 THEN "+p+"+? ELSE 0 END),0) FROM work_requests WHERE job_id=?"), backend.RequestMetadataBytes, jobID).Scan(&handoff)
	if err != nil {
		return
	}
	c := "c.payload_bytes"
	progress := "(SELECT json_extract(pipeline_runtime_state,'$.progress_id') FROM jobs WHERE id=w.job_id)"
	if s.Driver == "postgres" {
		progress = "(SELECT pipeline_runtime_state->>'progress_id' FROM jobs WHERE id=w.job_id)"
	}
	err = s.row(ctx, s.bind("SELECT COALESCE(SUM("+c+"),0),COALESCE(SUM(CASE WHEN (c.growth_reserved OR w.alignment_attempts>0 OR w.alignment_network_attempts>0 OR c.identity="+progress+") AND "+c+"<? THEN ?-"+c+" ELSE 0 END),0) FROM work_candidates c JOIN work_items w ON w.id=c.work_item_id WHERE w.job_id=? AND c.state IN ('pending_alignment','ready_to_commit')"), s.ItemBytes, s.ItemBytes, jobID).Scan(&candidates, &growth)
	return
}
