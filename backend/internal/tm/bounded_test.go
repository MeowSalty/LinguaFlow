package tm

import (
	"context"
	stdsql "database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/ent/runtime"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/tmentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	_ "modernc.org/sqlite"
)

func newBoundedTM(t *testing.T, legacyDuplicates bool) (*SQLite, *ent.Client, int) {
	t.Helper()
	db, e := stdsql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if e != nil {
		t.Fatal(e)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { client.Close() })
	if e = client.Schema.Create(context.Background()); e != nil {
		t.Fatal(e)
	}
	// Only this isolated fixture relaxes the modern unique hash index, so the
	// legacy Search ordering/all-success update contract can be compared.
	if legacyDuplicates {
		if _, e = db.Exec("DROP INDEX tmentry_scope_key_source_hash_source_lang_target_lang"); e != nil {
			t.Fatal(e)
		}
	}
	p, e := client.Project.Create().SetName("bounded tm").Save(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	m, e := NewSQLite(client, Scope{ProjectID: p.ID})
	if e != nil {
		t.Fatal(e)
	}
	return m, client, p.ID
}

func addBoundedTMRow(t *testing.T, client *ent.Client, projectID int, target string, usage int) int {
	t.Helper()
	row, e := client.TMEntry.Create().SetScopeKey("project:" + intString(projectID)).SetSourceHash(hash.Full("source")).SetSourceText("source").SetTargetText(target).SetSourceLang("en").SetTargetLang("zh").SetProjectID(projectID).SetUsageCount(usage).Save(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return row.ID
}

func intString(n int) string {
	var b [32]byte
	at := len(b)
	for n > 0 {
		at--
		b[at] = byte('0' + n%10)
		n /= 10
	}
	return string(b[at:])
}

func TestBoundedTMOrderAndUsageParity(t *testing.T) {
	m, client, projectID := newBoundedTM(t, true)
	ids := []int{addBoundedTMRow(t, client, projectID, "a", 2), addBoundedTMRow(t, client, projectID, "b", 8), addBoundedTMRow(t, client, projectID, "c", 8)}
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	held := false
	run := func(_ context.Context, fn func() error) error {
		if held {
			t.Fatal("nested CPU")
		}
		held = true
		defer func() { held = false }()
		return fn()
	}
	var got []Match
	e := m.VisitMatchesBounded(context.Background(), " source ", "en", "zh", b, run, func(match Match) error {
		if held {
			t.Fatal("yield holds CPU")
		}
		got = append(got, match)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	want := []Match{{Source: "source", Target: "b", Score: 1}, {Source: "source", Target: "c", Score: 1}, {Source: "source", Target: "a", Score: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i, id := range ids {
		row := client.TMEntry.GetX(context.Background(), id)
		if row.UsageCount != []int{3, 9, 9}[i] {
			t.Fatalf("usage=%d", row.UsageCount)
		}
	}
}

func TestBoundedTMRejectsSQLByteHeaderBeforeBody(t *testing.T) {
	m, client, projectID := newBoundedTM(t, false)
	id := addBoundedTMRow(t, client, projectID, strings.Repeat("x", 64<<10), 7)
	b, _ := backend.NewBuildBounds(4096, 4096)
	yields := 0
	e := m.VisitMatchesBounded(context.Background(), "source", "en", "zh", b, nil, func(Match) error { yields++; return nil })
	if !errors.Is(e, backend.ErrBuildCapacity) || yields != 0 {
		t.Fatalf("err=%v yields=%d", e, yields)
	}
	if row := client.TMEntry.GetX(context.Background(), id); row.UsageCount != 7 {
		t.Fatal("capacity rejection updated usage")
	}
}

func TestBoundedTMInputCreditRefusalPrecedesYieldAndUsage(t *testing.T) {
	m, client, projectID := newBoundedTM(t, false)
	id := addBoundedTMRow(t, client, projectID, "target", 7)
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	want := errors.New("input attachment unavailable")
	claims, yields := 0, 0
	ctx := WithInputReadCredit(context.Background(), func(n int64) error {
		claims++
		if n != int64(len("source")+len("target")) {
			t.Fatalf("read bytes=%d", n)
		}
		return want
	})
	err := m.VisitMatchesBounded(ctx, "source", "en", "zh", b, nil, func(Match) error {
		yields++
		return nil
	})
	if !errors.Is(err, want) || claims != 1 || yields != 0 {
		t.Fatalf("err=%v claims=%d yields=%d", err, claims, yields)
	}
	if got := client.TMEntry.GetX(context.Background(), id).UsageCount; got != 7 {
		t.Fatalf("usage=%d", got)
	}
}

func TestBoundedTMVisitorFailureRollsBackUsage(t *testing.T) {
	m, client, projectID := newBoundedTM(t, true)
	ids := []int{addBoundedTMRow(t, client, projectID, "a", 3), addBoundedTMRow(t, client, projectID, "b", 3)}
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	calls := 0
	e := m.VisitMatchesBounded(context.Background(), "source", "en", "zh", b, nil, func(Match) error {
		calls++
		if calls == 2 {
			return backend.ErrBuildCapacity
		}
		return nil
	})
	if !errors.Is(e, backend.ErrBuildCapacity) {
		t.Fatalf("err=%v", e)
	}
	for _, id := range ids {
		usage, e := client.TMEntry.Query().Where(tmentry.IDEQ(id)).First(context.Background())
		if e != nil || usage.UsageCount != 3 {
			t.Fatalf("usage=%v err=%v", usage, e)
		}
	}
}
