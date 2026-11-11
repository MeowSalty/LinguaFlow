package worker

import (
	"bytes"
	"context"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// persistStoredEncodedBatch keeps only one raw response and one encoded issue
// value across each DB callback. Per-member trees die inside the build permit.
func (r *JobRunner) persistStoredEncodedBatch(ctx context.Context, mode string, payload []byte, doc *pipeline.Document, rows []*ent.Segment, ids map[int]int, resource int, generation int64) (int, error) {
	runtime := pipeline.ExecutionRuntimeFromContext(ctx)
	if runtime == nil {
		return 0, fmt.Errorf("stored result has no execution runtime")
	}
	prefix := []byte("{\"Segments\":[")
	if !bytes.HasPrefix(payload, prefix) {
		return 0, fmt.Errorf("invalid stored result payload")
	}
	pos := len(prefix)
	completed := 0
	for {
		if pos >= len(payload) {
			return completed, fmt.Errorf("truncated stored result")
		}
		if payload[pos] == ']' {
			if !bytes.Equal(payload[pos:], []byte("]}")) {
				return completed, fmt.Errorf("trailing stored result")
			}
			return completed, nil
		}
		var idx, consumed int
		var issuePayload []byte
		err := runtime.Run(ctx, func() error {
			b, e := backend.NewBuildBounds(runtime.ResponseBytes, runtime.HandoffBytes)
			if e != nil {
				return e
			}
			raw, n, e := backend.FirstJSONRaw(b, payload[pos:])
			if e != nil {
				return e
			}
			consumed = n
			var ts pipeline.TranslatedSegment
			if e = b.DecodeJSONLimited(raw, &ts, runtime.ResponseBytes); e != nil {
				return e
			}
			idx = ts.Index
			if idx < 0 || idx >= len(doc.Segments) || rows != nil && idx >= len(rows) {
				return fmt.Errorf("stored member outside document")
			}
			issues := ts.Issues
			if mode == "semantic_qa" {
				if issues == nil {
					return nil
				}
				if e = b.ReserveNodes(int64(len(doc.Segments[idx].Issues)+len(issues))*8, 128); e != nil {
					return e
				}
				reconciled, err := qa.ReconcileIssuesBounded(b, issues, doc.Segments[idx].Issues)
				if err != nil {
					return err
				}
				issues, e = mergeSemanticQAIssuesBounded(b, doc.Segments[idx].Issues, reconciled)
				if e != nil {
					return e
				}
			}
			issuePayload, e = b.EncodeJSON(issues, runtime.ResponseBytes)
			return e
		})
		if err != nil {
			return completed, err
		}
		if issuePayload != nil {
			dbid, ok := canonicalDBIdentity(doc, idx)
			if ids != nil {
				dbid, ok = ids[idx]
			}
			if !ok {
				return completed, fmt.Errorf("stored member has no database identity")
			}
			var changed int
			err = withJobResourceTranslation(ctx, r.client, resource, generation, func(client *ent.Client) error {
				q := client.Segment.Update().Where(segment.IDEQ(dbid), segment.ContentVersionEQ(doc.Segments[idx].ContentVersion)).SetQualityIssues(nil).Modify(func(u *sql.UpdateBuilder) {
					u.Set(segment.FieldQualityIssues, sql.ExprFunc(func(b *sql.Builder) {
						b.WriteString("CAST(").Arg(issuePayload)
						if b.Dialect() == dialect.Postgres {
							b.WriteString(" AS JSONB)")
						} else {
							b.WriteString(" AS TEXT)")
						}
					}))
				})
				if mode == "semantic_qa" {
					q.Where(segment.StatusIn(service.SegmentStatusTranslated, service.SegmentStatusEdited))
					if doc.Segments[idx].TargetIsNull {
						q.Where(segment.TargetTextIsNil())
					} else {
						q.Where(segment.TargetTextEQ(doc.Segments[idx].Target))
					}
				}
				var e error
				changed, e = q.Save(ctx)
				return e
			})
			if err != nil {
				return completed, err
			}
			if changed > 0 {
				completed++
			}
		}
		issuePayload = nil
		pos += consumed
		if pos < len(payload) && payload[pos] == ',' {
			pos++
		}
	}
}
