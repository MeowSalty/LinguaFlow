package worker

import (
	"encoding/json"
	"reflect"
	"testing"

	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	pa "github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestPersistenceStoredEncodedQualityIssues(t *testing.T) {
	for _, issues := range [][]qa.QualityIssue{nil, {{Code: "calque", Severity: qa.SeverityWarning, Message: "中文 \"quoted\"", Disposition: qa.DispositionPending}}} {
		name := "null"
		if issues != nil {
			name = "issues"
		}
		t.Run(name, func(t *testing.T) {
			fixture := pa.Seed(t, 1, true)
			admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
			if err != nil {
				t.Fatal(err)
			}
			runtime := pipeline.NewExecutionRuntime(admission, pipeline.DefaultCandidateLimits(), nil)
			defer runtime.Close()
			ctx := pipeline.WithExecutionRuntime(t.Context(), runtime)
			row := fixture.Client.Segment.GetX(ctx, fixture.IDs[0])
			doc := &pipeline.Document{Segments: []pipeline.Segment{{DBID: row.ID, ContentVersion: row.ContentVersion}}}
			payload, err := json.Marshal(struct {
				Segments []pipeline.TranslatedSegment
			}{Segments: []pipeline.TranslatedSegment{{Index: 0, Issues: issues}}})
			if err != nil {
				t.Fatal(err)
			}
			runner := &JobRunner{client: fixture.Client}
			completed, err := runner.persistStoredEncodedBatch(ctx, "qa", payload, doc, nil, nil, fixture.Scope.ResourceID, 0)
			if err != nil || completed != 1 {
				t.Fatalf("encoded stored update: completed=%d err=%v", completed, err)
			}
			stored := fixture.Client.Segment.GetX(ctx, row.ID)
			if stored.ContentVersion != row.ContentVersion+1 || !reflect.DeepEqual(stored.QualityIssues, issues) {
				t.Fatalf("stored quality JSON was not preserved: %+v", stored)
			}
			isNull := fixture.Client.Segment.Query().Where(segment.IDEQ(row.ID), func(sel *sql.Selector) {
				sel.Where(sql.IsNull(sel.C(segment.FieldQualityIssues)))
			}).ExistX(ctx)
			if isNull {
				t.Fatal("encoded JSON null unexpectedly became SQL NULL")
			}
			completed, err = runner.persistStoredEncodedBatch(ctx, "qa", payload, doc, nil, nil, fixture.Scope.ResourceID, 0)
			if err != nil || completed != 0 {
				t.Fatalf("stale content version wrote encoded issues: completed=%d err=%v", completed, err)
			}
		})
	}
}
