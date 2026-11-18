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
func (s InventorySource) storageRowBytes(ctx context.Context, client *ent.Client, key storageRowKey) (int64, error) {
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "settle", Operation: "row_length", Rows: 1})
	fields := storageTableFields(key.table)
	if key.table == "work_requests" {
		var err error
		fields, err = s.requestStorageFields(ctx)
		if err != nil {
			return 0, err
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
	rows, err := client.QueryContext(ctx, s.bind("SELECT "+strings.Join(lengths, "+")+" FROM "+key.table+" WHERE id=?"), key.id)
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
func (b *StorageBudget) recordStorageBefore(ctx context.Context, client *ent.Client, m ent.Mutation) (storageRowKey, error) {
	table := mutationStorageTable(m)
	id, ok := mutationRowID(m, nil)
	key := storageRowKey{table: table, id: id}
	if m.Op().Is(ent.OpCreate) {
		return key, nil
	}
	if !ok {
		if many, yes := m.(interface {
			IDs(context.Context) ([]int, error)
		}); yes {
			ids, err := many.IDs(ctx)
			if err != nil {
				return key, err
			}
			for _, n := range ids {
				k := storageRowKey{table: table, id: n}
				if _, exists := b.changes[k]; exists {
					continue
				}
				size, err := b.Source.storageRowBytes(ctx, client, k)
				if err != nil {
					return key, err
				}
				b.changes[k] = storageRowChange{before: size}
			}
		}
		return key, nil
	}
	if _, exists := b.changes[key]; exists {
		return key, nil
	}
	n, err := b.Source.storageRowBytes(ctx, client, key)
	if err == nil {
		b.changes[key] = storageRowChange{before: n}
	}
	return key, err
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
