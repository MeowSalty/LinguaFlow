package workstate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

const storageInventoryPage = 16

type storageInventoryRow struct {
	bytes int64
}

// storageInventoryByIDs reads physical lengths through the current SQL client.
// Callers retain the inventory lock; before reads also use the write transaction.
// Only scalar facts are returned, with at most one bounded ID page per query.
func (s InventorySource) storageInventoryByIDs(ctx context.Context, client *ent.Client, table string, ids []int) (map[int]storageInventoryRow, error) {
	if len(ids) > storageInventoryPage {
		return nil, fmt.Errorf("storage inventory page exceeds %d rows", storageInventoryPage)
	}
	switch table {
	case "work_requests", "work_candidates", "work_request_input_members", "work_response_members":
	default:
		return nil, fmt.Errorf("unsupported storage inventory table %q", table)
	}
	out := make(map[int]storageInventoryRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	lengths, err := s.storageRowLengths(ctx, storageRowKey{table: table})
	if err != nil {
		return nil, err
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := "SELECT id," + lengths + " FROM " + table + " WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "settle", Operation: "row_length", Rows: int64(len(ids))})
	rows, err := client.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var fact storageInventoryRow
		if err = rows.Scan(&id, &fact.bytes); err != nil {
			break
		}
		out[id] = fact
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return out, nil
}
