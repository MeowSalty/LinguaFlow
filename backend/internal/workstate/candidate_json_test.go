package workstate

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestCandidateJSONBRepresentationKeepsCanonicalBudget(t *testing.T) {
	canonical := []byte(`{"count":9007199254740993,"dto_version":2,"target":"中文 <ruby> &","value":1e+30}`)
	want, err := CanonicalCandidateJSON(canonical, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	normalized := []byte(`{ "value": 1000000000000000000000000000000, "target": "中文 <ruby> &", "dto_version": 2, "count": 9007199254740993 }`)
	got, err := CanonicalCandidateJSON(normalized, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("JSONB spelling changed canonical DTO: %s / %s", got, want)
	}
	if !bytes.Contains(got, []byte("9007199254740993")) {
		t.Fatal("normalization rounded an integer identity")
	}
}

func TestCandidateJSONBTypedNumberCanonicalRoundTrip(t *testing.T) {
	value := struct {
		ID       int64          `json:"id"`
		Unsigned uint64         `json:"unsigned"`
		Meta     map[string]any `json:"meta"`
	}{ID: 9007199254740993, Unsigned: math.MaxUint64, Meta: map[string]any{"near_integer": float64(1e18), "large": float64(1e30), "single_precision": float32(0.1), "positive_zero": float64(0), "negative_zero": math.Copysign(0, -1), "negative_min": -math.SmallestNonzeroFloat64}}
	b, err := backend.NewBuildBounds(1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := b.EncodeJSON(value, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	normalized := string(canonical)
	for _, n := range []float64{1e18, 1e30, -math.SmallestNonzeroFloat64} {
		normalized = strings.ReplaceAll(normalized, strconv.FormatFloat(n, 'g', -1, 64), strconv.FormatFloat(n, 'f', -1, 64))
	}
	normalized = strings.ReplaceAll(normalized, `"negative_zero":-0`, `"negative_zero":0`)
	want, err := CanonicalCandidateJSON(canonical, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalCandidateJSON([]byte(normalized), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || len(want) != len(canonical) {
		t.Fatalf("typed numeric canonical/Q changed: original=%s canonical=%s normalized=%s", canonical, want, got)
	}
	for _, identity := range []string{"9007199254740993", "18446744073709551615"} {
		if !bytes.Contains(got, []byte(identity)) {
			t.Fatalf("lost typed integer identity %s", identity)
		}
	}
}

func TestCandidateACKSQLUsesScalarJSONEquality(t *testing.T) {
	for _, driver := range []string{"sqlite3", "postgres"} {
		sel := sql.Dialect(driver).Select("id").From(sql.Table("work_candidates"))
		sel.Where(candidateJSONEquality(sel, []byte(`{"id":9007199254740993}`)))
		query, args := sel.Query()
		if len(args) != 1 || args[0] != `{"id":9007199254740993}` {
			t.Fatalf("SQL parameter altered exact numeric fact: %v", args)
		}
		if driver == "postgres" && !strings.Contains(query, "CAST($1 AS JSONB)") {
			t.Fatal(query)
		}
		if driver != "postgres" && !strings.Contains(query, "json(?)") {
			t.Fatal(query)
		}
		if strings.Contains(query, "SELECT \"payload\"") {
			t.Fatal("ACK copied body instead of returning scalar equality")
		}
	}
}

func TestBudgetIntegrationJSONBPromotionAndCandidateReplay(t *testing.T) {
	ctx, client, store, scope, seg := fixture(t)
	currentExecution(t, ctx, client, scope)
	r, h, c := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	normalize := func(table, field string, payload []byte) {
		t.Helper()
		var out bytes.Buffer
		if err := json.Indent(&out, payload, "", "  "); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ExecContext(ctx, "UPDATE "+table+" SET "+field+"=$1", out.String()); err != nil {
			t.Fatal(err)
		}
	}
	members := client.WorkRequest.Query().OnlyX(ctx).PromotionMembers
	normalize("work_requests", "promotion_members", members)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatalf("normalized reservation replay: %v", err)
	}
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatalf("first handoff over normalized reservation: %v", err)
	}
	normalize("work_requests", "promotion_members", members)
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatalf("normalized handoff ACK/replay: %v", err)
	}
	p := MemberPromotion{Scope: scope, RequestID: r.ID, Cursor: 0, Candidate: &c, Next: cursorFromRow(client.WorkItem.Query().OnlyX(ctx))}
	if err := store.PromoteMember(ctx, p); err != nil {
		t.Fatal(err)
	}
	normalize("work_candidates", "payload", c.Payload)
	if err := store.PromoteMember(ctx, p); err != nil {
		t.Fatalf("normalized promoted candidate ACK/replay: %v", err)
	}
}

func TestBudgetIntegrationResponseMetadataRejectedBeforeBody(t *testing.T) {
	for _, field := range requestMetadataFields {
		t.Run(field, func(t *testing.T) {
			ctx, client, store, scope, seg := fixture(t)
			currentExecution(t, ctx, client, scope)
			r, h, _ := rawRequest(scope, seg)
			if err := store.ReserveRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			if err := store.HandoffResponse(ctx, h); err != nil {
				t.Fatal(err)
			}
			var malformed any
			switch field {
			case "promotion_status", "corrected_target_index":
				malformed = bytes.Repeat([]byte{0}, 40000)
			case "segment_ids":
				malformed = "[" + strings.Repeat("1,", 65536) + "1]"
			default:
				malformed = `{"padding":"` + strings.Repeat("x", 40000) + `"}`
			}
			if _, err := client.ExecContext(ctx, "UPDATE work_requests SET "+field+"=$1 WHERE identity=$2", malformed, r.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadResponse(ctx, r.ID, 1<<20, 1<<20, 1<<20); err == nil {
				t.Fatal("unbounded metadata returned a response body")
			}
			if _, err := store.LoadResponseMember(ctx, r.ID, 0); err == nil {
				t.Fatal("unbounded metadata returned promotion member")
			}
		})
	}
}

func TestBudgetIntegrationJSONBReadLengthIndependentOfCanonicalBytes(t *testing.T) {
	ctx, c, store, scope, seg := fixture(t)
	w := c.WorkItem.Query().OnlyX(ctx)
	canonical := []byte(`{"dto_version":2,"target":"target","version":1}`)
	var normalized bytes.Buffer
	if err := json.Indent(&normalized, canonical, "", "  "); err != nil {
		t.Fatal(err)
	}
	stored := normalized.Bytes()
	if len(stored) == len(canonical) {
		t.Fatal("fixture does not model JSONB representation difference")
	}
	c.WorkCandidate.Create().SetIdentity("jsonb-spelling").SetWorkItemID(w.ID).SetDtoVersion(2).SetSnapshotDigest("frozen").SetMode("translate").SetSourceGeneration(0).SetBaselineVersion(seg.ContentVersion).SetBaselineStatus(string(seg.Status)).SetPayload(stored).SetPayloadBytes(int64(len(canonical))).SaveX(ctx)
	// Ent's SQLite JSON encoder compacts RawMessage. Replace the SQL value
	// with a PostgreSQL-style normalized representation without changing Q.
	if _, err := c.ExecContext(ctx, "UPDATE work_candidates SET payload=$1 WHERE identity=$2", string(stored), "jsonb-spelling"); err != nil {
		t.Fatal(err)
	}
	var reserved int64
	row, err := store.LoadCandidatePayloadReserved(ctx, scope, "jsonb-spelling", 1<<20, func(n int64) (func(), error) { reserved = n; return func() {}, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reserved != int64(len(stored)) || !bytes.Equal(row.Payload, canonical) {
		t.Fatalf("actual read/canonical budget conflated: reserved=%d stored=%d row=%s", reserved, len(stored), row.Payload)
	}
}

func TestInventorySQLUsesDeclaredColumnTypes(t *testing.T) {
	s := InventorySource{Driver: "postgres"}
	for _, column := range []string{"freeze_payload", "response_payload", "corrected_targets", "corrected_target_index", "promotion_status", "c.pipeline_header"} {
		if got := s.length(column); got != "COALESCE(OCTET_LENGTH("+column+"),0)" {
			t.Fatalf("bytea %s: %s", column, got)
		}
	}
	for _, column := range []string{"c.payload", "members", "debits", "glossary_receipt", "promotion_members", "segment_ids", "c.baseline_target"} {
		got := s.length(column)
		if !strings.Contains(got, "OCTET_LENGTH(CAST("+column+" AS TEXT))") {
			t.Fatalf("JSON/text %s: %s", column, got)
		}
	}
	s.Driver = "sqlite"
	if got := s.length("response_payload"); got != "COALESCE(LENGTH(CAST(response_payload AS BLOB)),0)" {
		t.Fatal(got)
	}
}
