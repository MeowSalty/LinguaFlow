package authrules

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testField(kind string) Field {
	return Field{FieldID: "custom.value", Label: "Value", Type: kind, Source: "oidc_id_token", Pointer: "/value"}
}
func testRule(op, value string) RuleSet {
	return RuleSet{Mode: "conditions", Match: "all", Conditions: []Condition{{FieldID: "custom.value", Operator: op, Value: json.RawMessage(value)}}}
}
func TestAuthRulesTypesAndPrecision(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ kind, actual, op, want, status string }{
		{"string", `"A"`, "eq", `"a"`, "fail"}, {"string", `"A"`, "in", `["A","B"]`, "pass"},
		{"boolean", `false`, "eq", `false`, "pass"}, {"boolean", `"false"`, "eq", `false`, "unknown"},
		{"integer", `9007199254740993`, "gt", `9007199254740992`, "pass"}, {"integer", `1.0`, "eq", `1`, "unknown"},
		{"integer", `-9007199254740993`, "lt", `-9007199254740992`, "pass"},
		{"number", `0.10000000000000000001`, "gt", `0.1`, "pass"}, {"number", `1e308`, "gt", `1e307`, "pass"}, {"number", `1e309`, "eq", `1`, "unknown"},
		{"datetime", `"2026-10-12T08:00:00+08:00"`, "gte", `"2026-10-12T00:00:00Z"`, "pass"}, {"datetime", `"2026-10-12"`, "gte", `"2026-10-12T00:00:00Z"`, "unknown"},
		{"string_list", `["ops","engineering"]`, "contains_all", `["engineering","ops"]`, "pass"}, {"string_list", `[]`, "not_contains", `"ops"`, "pass"},
		{"string_list", `["ops",1]`, "contains", `"ops"`, "unknown"}, {"string", `null`, "ne", `"blocked"`, "unknown"},
	} {
		t.Run(tc.kind+"/"+tc.actual+"/"+tc.op, func(t *testing.T) {
			fields := []Field{testField(tc.kind)}
			rule := testRule(tc.op, tc.want)
			attrs, err := Extract("oidc", fields, map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"value":` + tc.actual + `}`)}, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			got := Evaluate("oidc", rule, fields, Evidence{ObservedAt: now, Attributes: attrs}, now)
			if got.Status != tc.status {
				t.Fatalf("got %+v want %s", got, tc.status)
			}
		})
	}
}
func TestAuthRulesThreeStateTruthTable(t *testing.T) {
	fields := []Field{testField("string"), {FieldID: "custom.other", Label: "Other", Type: "string", Source: "oidc_userinfo", Pointer: "/value"}}
	now := time.Now().UTC()
	for _, match := range []string{"all", "any"} {
		for _, left := range []string{"pass", "fail", "unknown"} {
			for _, right := range []string{"pass", "fail", "unknown"} {
				t.Run(match+"/"+left+"/"+right, func(t *testing.T) {
					attrs := map[string]Attribute{}
					for i, status := range []string{left, right} {
						a := Attribute{Status: "pass", Fingerprint: FingerprintField(fields[i]), Value: json.RawMessage(`"yes"`)}
						if status == "fail" {
							a.Value = json.RawMessage(`"no"`)
						} else if status == "unknown" {
							a.Status = "unknown"
							a.Reason = "source_unavailable"
						}
						attrs[fields[i].FieldID] = a
					}
					rule := testRule("eq", `"yes"`)
					rule.Match = match
					rule.Conditions = append(rule.Conditions, Condition{FieldID: "custom.other", Operator: "eq", Value: json.RawMessage(`"yes"`)})
					want := "unknown"
					if match == "all" {
						if left == "fail" || right == "fail" {
							want = "fail"
						} else if left == "pass" && right == "pass" {
							want = "pass"
						}
					} else {
						if left == "pass" || right == "pass" {
							want = "pass"
						} else if left == "fail" && right == "fail" {
							want = "fail"
						}
					}
					got := Evaluate("oidc", rule, fields, Evidence{Attributes: attrs}, now)
					if got.Status != want || got.Retryable != (want == "unknown") {
						t.Fatalf("got %+v want %s", got, want)
					}
				})
			}
		}
	}
	for _, tc := range []struct{ kind, op string }{{"string", "ne"}, {"string_list", "not_contains"}} {
		got := Evaluate("oidc", testRule(tc.op, `"blocked"`), []Field{testField(tc.kind)}, Evidence{}, now)
		if got.Status != "unknown" {
			t.Fatalf("missing negation passed: %+v", got)
		}
	}
}
func TestAuthRulesPointerAndStrictJSON(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"nested":{"a":1,"\u0061":2}}`, `{} {}`, strings.Repeat("[", 34) + strings.Repeat("]", 34), `{"a":NaN}`} {
		if ValidateJSON([]byte(raw)) == nil {
			t.Errorf("accepted invalid JSON %s", raw)
		}
	}
	now := time.Now().UTC()
	source := json.RawMessage(`{"a/b":{"~key":["zero","one"]},"":2}`)
	for _, tc := range []struct{ pointer, status string }{{"/a~1b/~0key/1", "pass"}, {"/a~1b/~0key/01", "unknown"}, {"/a~1b/~0key/-1", "unknown"}, {"/a~1b/~0key/-", "unknown"}, {"/a~1b/~0key/999999999999999999999999999", "unknown"}, {"/a~1b/~0key/*", "unknown"}} {
		f := testField("string")
		f.Pointer = tc.pointer
		a, err := Extract("oidc", []Field{f}, map[string]json.RawMessage{"oidc_id_token": source}, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if a[f.FieldID].Status != tc.status {
			t.Fatalf("pointer %s: %+v", tc.pointer, a)
		}
	}
	for _, pointer := range []string{"", "value", "/bad~", "/bad~2", "/" + strings.Repeat("a/", 17)} {
		f := testField("string")
		f.Pointer = pointer
		if Validate("oidc", []Field{f}, Unrestricted()) == nil {
			t.Errorf("accepted pointer %q", pointer)
		}
	}
	f := testField("string")
	other := f
	other.FieldID = "custom.second"
	if Validate("oidc", []Field{f, other}, Unrestricted()) == nil {
		t.Fatal("ambiguous mapping admitted")
	}
	if _, err := Extract("oidc", []Field{f}, map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"value":"a","value":"b"}`)}, nil, now); err == nil {
		t.Fatal("duplicate source produced attributes")
	}
	for _, r := range []RuleSet{{}, {Mode: "conditions", Match: "all"}, {Mode: "conditions", Match: "nested", Conditions: testRule("eq", `"a"`).Conditions}, testRule("regex", `".*"`), testRule("eq", `1`), testRule("eq", `null`)} {
		if Validate("oidc", []Field{f}, r) == nil {
			t.Errorf("invalid rule admitted: %+v", r)
		}
	}
}
func TestAuthRulesAgeAndEvidenceBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	f := Field{FieldID: "external.account_created_at", Label: "Created", Type: "datetime", Source: "oidc_id_token", Pointer: "/created"}
	rule := RuleSet{Mode: "conditions", Match: "all", Conditions: []Condition{{FieldID: "external.account_age_days", Operator: "gte", Value: json.RawMessage(`365`)}}}
	for _, tc := range []struct {
		name    string
		created time.Time
		want    string
	}{{"exact", now.AddDate(0, 0, -365), "pass"}, {"short", now.AddDate(0, 0, -365).Add(time.Nanosecond), "fail"}, {"future", now.Add(time.Nanosecond), "unknown"}, {"ancient", time.Date(1500, 1, 1, 0, 0, 0, 0, time.UTC), "pass"}} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"created": tc.created.Format(time.RFC3339Nano)})
			attrs, err := Extract("oidc", []Field{f}, map[string]json.RawMessage{"oidc_id_token": raw}, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			e := Evidence{ObservedAt: now, ExpiresAt: now.Add(time.Hour), Attributes: attrs}
			got := Evaluate("oidc", rule, []Field{f}, e, now)
			if got.Status != tc.want {
				t.Fatalf("got %+v want %s", got, tc.want)
			}
			if tc.name == "short" && Evaluate("oidc", rule, []Field{f}, e, now.Add(time.Nanosecond)).Status != "pass" {
				t.Fatal("derived value not recomputed")
			}
			if Evaluate("oidc", rule, []Field{f}, e, e.ExpiresAt).Status != "unknown" {
				t.Fatal("expiry accepted")
			}
			if Evaluate("oidc", rule, []Field{f}, e, now.Add(-time.Nanosecond)).Status != "unknown" {
				t.Fatal("future observation accepted")
			}
		})
	}
	renamed := f
	renamed.Label = "Renamed"
	if RulesFingerprint("oidc", rule, []Field{f}) != RulesFingerprint("oidc", rule, []Field{renamed}) {
		t.Fatal("label invalidated login rules")
	}
	renamed.Pointer = "/different"
	if RulesFingerprint("oidc", rule, []Field{f}) == RulesFingerprint("oidc", rule, []Field{renamed}) {
		t.Fatal("dependency mapping failed to invalidate login rules")
	}
}
