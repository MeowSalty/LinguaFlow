package workstate

import (
	"os"
	"reflect"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestPersistenceCommitEncodedQualityIssues(t *testing.T) {
	driver := dialect.SQLite
	if os.Getenv("LINGUAFLOW_PERSISTENCE_DRIVER") == dialect.Postgres {
		driver = dialect.Postgres
	}
	issues := []qa.QualityIssue{{Code: "calque", Severity: qa.SeverityWarning, Message: "中文 \"quoted\"", Disposition: qa.DispositionPending}}
	for _, tc := range []struct {
		name    string
		payload []byte
		issues  []qa.QualityIssue
		sqlNull bool
	}{
		{name: "typed-null", sqlNull: true},
		{name: "encoded-null", payload: []byte("null")},
		{name: "encoded-issues", payload: []byte(`[{"code":"calque","severity":"warning","message":"中文 \"quoted\""}]`), issues: issues},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := independentClients(t, driver)
			ctx, _, store, scope, row := seedFixture(t, t.Context(), client)
			candidate := ready(scope, row)
			if err := store.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			commit := input(candidate)
			commit.IssuesPayload = tc.payload
			result, err := store.Commit(ctx, commit)
			if err != nil || result.Outcome != Committed {
				t.Fatalf("encoded commit: result=%+v err=%v", result, err)
			}
			stored := client.Segment.GetX(ctx, row.ID)
			if stored.ContentVersion != 2 || stored.TargetText == nil || *stored.TargetText != "translated" || !reflect.DeepEqual(stored.QualityIssues, tc.issues) {
				t.Fatalf("encoded issues changed content or JSON value: %+v", stored)
			}
			isNull := client.Segment.Query().Where(segment.IDEQ(row.ID), func(sel *sql.Selector) {
				sel.Where(sql.IsNull(sel.C(segment.FieldQualityIssues)))
			}).ExistX(ctx)
			if isNull != tc.sqlNull {
				t.Fatalf("SQL NULL=%t want %t; encoded JSON null must remain distinct", isNull, tc.sqlNull)
			}
			result, err = store.Commit(ctx, commit)
			if err != nil || result.Outcome != AlreadyCommitted || client.Job.GetX(ctx, scope.JobID).ProgressCompleted != 1 {
				t.Fatalf("encoded replay: result=%+v err=%v", result, err)
			}
		})
	}
}
