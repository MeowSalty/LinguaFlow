package v015

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

const projectionPageSize = 128
const projectionPathByteLimit int64 = 64 << 20

// publishedProjectionDigest covers only source IDs whose representation this
// schema step changed. Later legal account/data additions are outside its scope.
// The receipt is first recorded by the converting transaction, never rebuilt
// from a possibly changed target during resume or ready verification.
func publishedProjectionDigest(ctx context.Context, q baselineQueryer, p *operation.Plan) (string, error) {
	if p.Baseline == nil {
		return "", operation.Fail(operation.BackupIncomplete, "source projection scope is missing")
	}
	if err := p.Baseline.Bind(p); err != nil {
		return "", err
	}
	var taskMax, bindingMax int64
	for _, table := range p.Baseline.Tables {
		switch table.Name {
		case "sync_tasks":
			taskMax = int64(table.MaxID)
		case "login_bindings":
			bindingMax = int64(table.MaxID)
		}
	}
	pathBytes := "length(CAST(resource_path AS BLOB))"
	if p.Source.Driver == "postgres" {
		pathBytes = "octet_length(resource_path)"
	}
	type projection struct {
		label, query string
		max          int64
		columns      int
		pair, path   bool
	}
	projections := []projection{
		{"tasks", "SELECT id,checkpoint_version,total_updated,total_skipped FROM sync_tasks WHERE id>$1 AND id<=$2 ORDER BY id LIMIT 128", taskMax, 4, false, false},
		{"items", "SELECT task_id,ordinal,segment_id,resource_id FROM sync_task_items WHERE (task_id,ordinal)>($1,$2) AND task_id<=$3 ORDER BY task_id,ordinal LIMIT 128", taskMax, 4, true, false},
		{"results", fmt.Sprintf("SELECT task_id,resource_id,updated_count,skipped_count,CASE WHEN %s<=%d THEN resource_path ELSE NULL END,%s FROM sync_task_results WHERE (task_id,resource_id)>($1,$2) AND task_id<=$3 ORDER BY task_id,resource_id LIMIT 128", pathBytes, projectionPathByteLimit, pathBytes), taskMax, 6, true, true},
		{"binding_expiry", "SELECT id,evidence_expires_at FROM login_bindings WHERE id>$1 AND id<=$2 ORDER BY id LIMIT 128", bindingMax, 2, false, false},
	}
	h := sha256.New()
	buffer := make([]byte, 32<<10)
	for _, projection := range projections {
		writeProjectionValue(h, projection.label, buffer)
		writeProjectionValue(h, projection.max, buffer)
		var after, next int64
		for {
			args := []any{after, projection.max}
			if projection.pair {
				args = []any{after, next, projection.max}
			}
			rows, err := q.QueryContext(ctx, projection.query, args...)
			if err != nil {
				return "", err
			}
			count := 0
			for rows.Next() {
				values := make([]any, projection.columns)
				pointers := make([]any, len(values))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err = rows.Scan(pointers...); err != nil {
					break
				}
				if projection.path {
					bytes, ok := projectionInt(values[len(values)-1])
					if !ok || bytes < 0 || bytes > projectionPathByteLimit {
						err = operation.Fail(operation.UnsupportedSource, "converted resource path exceeds its physical byte limit")
						break
					}
				}
				var ok bool
				after, ok = projectionInt(values[0])
				if !ok {
					err = operation.Fail(operation.BaselineChanged, "invalid projection identity")
					break
				}
				if projection.pair {
					next, ok = projectionInt(values[1])
					if !ok {
						err = operation.Fail(operation.BaselineChanged, "invalid projection position")
						break
					}
				}
				for _, value := range values {
					if err = writeProjectionValue(h, value, buffer); err != nil {
						break
					}
				}
				if err != nil {
					break
				}
				count++
			}
			if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
				return "", err
			}
			if count < projectionPageSize {
				break
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func projectionInt(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// Length-prefixed values avoid a giant escaped JSON buffer for a legacy path.
// SHA-256's Write never errors; the only dynamic buffer is a fixed 32KiB chunk.
func writeProjectionValue(h hash.Hash, value any, buffer []byte) error {
	kind, text := "", ""
	switch v := value.(type) {
	case nil:
		kind = "n"
	case int64:
		kind = "i"
		text = strconv.FormatInt(v, 10)
	case int:
		kind = "i"
		text = strconv.Itoa(v)
	case string:
		kind = "s"
		text = v
	case []byte:
		h.Write([]byte("s" + strconv.Itoa(len(v)) + ":"))
		for len(v) > 0 {
			n := min(len(v), len(buffer))
			copy(buffer, v[:n])
			h.Write(buffer[:n])
			v = v[n:]
		}
		h.Write([]byte("\n"))
		return nil
	case time.Time:
		kind = "t"
		text = v.UTC().Format(time.RFC3339Nano)
	default:
		return operation.Fail(operation.BaselineChanged, "unsupported projection value type")
	}
	h.Write([]byte(kind + strconv.Itoa(len(text)) + ":"))
	for len(text) > 0 {
		n := min(len(text), len(buffer))
		copy(buffer, text[:n])
		h.Write(buffer[:n])
		text = text[n:]
	}
	h.Write([]byte("\n"))
	return nil
}

func verifyPublishedProjection(ctx context.Context, q baselineQueryer, p *operation.Plan) error {
	recorded, err := operation.StepResultDigest(ctx, q, p, "schema")
	if err != nil {
		return err
	}
	actual, err := publishedProjectionDigest(ctx, q, p)
	if err != nil {
		return err
	}
	if actual != recorded {
		return operation.Fail(operation.BaselineChanged, "converted sync plan, result totals or original binding expiry changed after its schema receipt")
	}
	return nil
}
