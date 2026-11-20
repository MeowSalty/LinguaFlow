package workstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

var memberInputStorageFields = []string{"payload", "prompt_id", "work_id", "candidate_id", "baseline_digest", "baseline_status", "payload_digest"}
var memberOutputStorageFields = []string{"payload", "corrected_payload", "payload_digest", "corrected_digest", "failure", "candidate_digest"}

func (s InventorySource) storageLength(table, field string) string {
	column := field
	if i := strings.LastIndexByte(column, '.'); i >= 0 {
		column = column[i+1:]
	}
	if s.Driver == "postgres" && (table == "work_request_input_members" || table == "work_response_members") && (column == "payload" || column == "corrected_payload") {
		return "COALESCE(OCTET_LENGTH(" + field + "),0)"
	}
	return s.length(field)
}
func storageTableFields(table string) []string {
	switch table {
	case "work_request_input_members":
		return memberInputStorageFields
	case "work_response_members":
		return memberOutputStorageFields
	case "work_candidates":
		return []string{"payload", "pipeline_header", "baseline_target"}
	default:
		return []string{"freeze_payload", "response_payload", "corrected_targets", "corrected_target_index", "glossary_receipt", "promotion_members", "promotion_status", "members", "debits", "segment_ids", "capture_payload", "response_glossary"}
	}
}
func (s InventorySource) storageRowLengths(ctx context.Context, key storageRowKey) (string, error) {
	fields := storageTableFields(key.table)
	if key.table == "work_requests" {
		var err error
		fields, err = s.requestStorageFields(ctx)
		if err != nil {
			return "", err
		}
	}
	var lengths []string
	for _, f := range fields {
		if key.table == "work_requests" {
			switch f {
			case "freeze_payload", "response_payload", "corrected_targets", "corrected_target_index", "promotion_members", "promotion_status":
				// These physical legacy columns are immutable to the final schema.
				// Initialization counts them; their constant contribution cancels
				// from every touched-row delta without reading it again.
				continue
			case "members", "debits", "segment_ids":
				lengths = append(lengths, "CASE WHEN response_format_version=2 THEN 0 ELSE "+s.storageLength(key.table, f)+" END")
				continue
			}
		}
		lengths = append(lengths, s.storageLength(key.table, f))
	}
	if key.table == "work_requests" {
		lengths = append(lengths, "CASE WHEN response_format_version=2 THEN request_metadata_bytes ELSE 0 END")
	}
	return strings.Join(lengths, "+"), nil
}
func (s InventorySource) storageRowBytes(ctx context.Context, client *ent.Client, key storageRowKey) (int64, error) {
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "settle", Operation: "row_length", Rows: 1})
	lengths, err := s.storageRowLengths(ctx, key)
	if err != nil {
		return 0, err
	}
	rows, err := client.QueryContext(ctx, s.bind("SELECT "+lengths+" FROM "+key.table+" WHERE id=?"), key.id)
	if err != nil {
		return 0, err
	}
	var n int64
	err = inventoryRow{rows: rows}.Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
func mutationStorageTable(m ent.Mutation) string {
	switch m.(type) {
	case *ent.WorkRequestMutation:
		return "work_requests"
	case *ent.WorkCandidateMutation:
		return "work_candidates"
	case *ent.WorkRequestInputMemberMutation:
		return "work_request_input_members"
	case *ent.WorkResponseMemberMutation:
		return "work_response_members"
	}
	return ""
}
func mutationRowID(m ent.Mutation, value ent.Value) (int, bool) {
	if id, ok := m.(interface{ ID() (int, bool) }); ok {
		if n, yes := id.ID(); yes {
			return n, true
		}
	}
	switch row := value.(type) {
	case *ent.WorkRequest:
		return row.ID, true
	case *ent.WorkCandidate:
		return row.ID, true
	case *ent.WorkRequestInputMember:
		return row.ID, true
	case *ent.WorkResponseMember:
		return row.ID, true
	}
	return 0, false
}

// A first touched-row read may establish the current mutation's request
// identity together with its physical byte baseline. No identity survives
// this hook call: later mutations, even on the same row, take the old path.
func storageBeforeIdentity(m ent.Mutation, requestID string) string {
	if requestID == "" || m.Op() != ent.OpUpdateOne || len(m.ClearedFields()) != 0 || len(m.AddedEdges()) != 0 || len(m.RemovedEdges()) != 0 || len(m.ClearedEdges()) != 0 {
		return ""
	}
	transactional, ok := m.(interface{ Tx() (*ent.Tx, error) })
	if !ok {
		return ""
	}
	if _, err := transactional.Tx(); err != nil {
		return ""
	}
	// Keep this a conservative scalar allowlist. New schema fields, identity
	// evidence and physical charge changes must retain the existing query.
	for _, field := range append(m.Fields(), m.AddedFields()...) {
		switch field {
		case "updated_at", "state", "promotion_cursor", "promotion_state", "archive_cursor", "remaining_members", "input_member_bytes", "output_member_bytes", "model_output_bytes", "correction_bytes", "member_storage_bytes", "public_bytes", "payload_bytes", "corrected_bytes", "outcome_kind", "outcome_limit", "outcome_observed":
		default:
			return ""
		}
	}
	switch mutationStorageTable(m) {
	case "work_requests":
		return "identity"
	case "work_candidates":
		return "parent_request_id"
	case "work_request_input_members", "work_response_members":
		return "(SELECT identity FROM work_requests WHERE id=request_id)"
	}
	return ""
}

func (b *StorageBudget) recordStorageBefore(ctx context.Context, client *ent.Client, m ent.Mutation, requestID string) (storageRowKey, bool, error) {
	table := mutationStorageTable(m)
	id, ok := mutationRowID(m, nil)
	key := storageRowKey{table: table, id: id}
	if m.Op().Is(ent.OpCreate) {
		return key, false, nil
	}
	if !ok {
		if many, yes := m.(interface {
			IDs(context.Context) ([]int, error)
		}); yes {
			ids, err := many.IDs(ctx)
			if err != nil {
				return key, false, err
			}
			for _, n := range ids {
				k := storageRowKey{table: table, id: n}
				if _, exists := b.changes[k]; exists {
					continue
				}
				size, err := b.Source.storageRowBytes(ctx, client, k)
				if err != nil {
					return key, false, err
				}
				b.changes[k] = storageRowChange{before: size}
			}
		}
		return key, false, nil
	}
	if _, exists := b.changes[key]; exists {
		return key, false, nil
	}
	if identity := storageBeforeIdentity(m, requestID); identity != "" {
		lengths, err := b.Source.storageRowLengths(ctx, key)
		if err != nil {
			return key, false, err
		}
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "settle", Operation: "row_length", Rows: 1})
		rows, err := client.QueryContext(ctx, b.Source.bind("SELECT "+lengths+","+identity+" FROM "+table+" WHERE id=?"), id)
		if err != nil {
			return key, false, err
		}
		var n int64
		var stableID string
		err = (inventoryRow{rows: rows}).Scan(&n, &stableID)
		found := err == nil
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		if err == nil {
			b.changes[key] = storageRowChange{before: n}
		}
		return key, found && stableID == requestID, err
	}
	n, err := b.Source.storageRowBytes(ctx, client, key)
	if err == nil {
		b.changes[key] = storageRowChange{before: n}
	}
	return key, false, err
}
func (b *StorageBudget) actualChangedStorage(ctx context.Context) (int64, error) {
	var delta int64
	client, ok := b.Source.DB.(*ent.Client)
	if !ok {
		return 0, fmt.Errorf("storage inventory requires SQL client")
	}
	for key, change := range b.changes {
		after, err := b.Source.storageRowBytes(ctx, client, key)
		if err != nil {
			return 0, err
		}
		delta += after - change.before
	}
	if delta < 0 && -delta > b.before {
		return 0, fmt.Errorf("invalid storage inventory delta")
	}
	return b.before + delta, nil
}

func (s InventorySource) requestStorageFields(ctx context.Context) ([]string, error) {
	columns := s.Columns
	if columns == nil {
		columns = map[string]bool{}
		query := "PRAGMA table_info(work_requests)"
		if s.Driver == "postgres" {
			query = "SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='work_requests'"
		}
		rows, err := s.DB.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			if s.Driver == "postgres" {
				err = rows.Scan(&name)
			} else {
				var cid, notnull, pk int
				var typ string
				var def any
				err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk)
			}
			if err != nil {
				break
			}
			columns[name] = true
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return nil, err
		}
	}
	var out []string
	for _, f := range storageTableFields("work_requests") {
		if columns[f] {
			out = append(out, f)
		}
	}
	return out, nil
}
