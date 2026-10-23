package v015

import "testing"

func TestCheckComparisonPreservesGroupingAndPostgresRewrites(t *testing.T) {
	for _, pair := range [][2]string{
		{"generation BETWEEN 1 AND 9007199254740991", "((generation >= 1) AND (generation <= '9007199254740991'::bigint))"},
		{"state IN ('configuring', 'enabled', 'disabled')", "((state)::text = ANY ((ARRAY['configuring'::character varying, 'enabled'::character varying, 'disabled'::character varying])::text[]))"},
		{"kind = 'password' AND (state IS NULL OR archived_at IS NULL)", "((kind)::text = 'password'::text) AND ((archived_at IS NULL) OR (state IS NULL))"},
	} {
		want, err := canonicalCheck(pair[0], "postgres")
		if err != nil {
			t.Fatal(err)
		}
		got, err := canonicalCheck(pair[1], "postgres")
		if err != nil || want != got {
			t.Fatalf("PostgreSQL decompiled check differs: %s -> %s; %s -> %s (%v)", pair[0], want, pair[1], got, err)
		}
	}
	want, err := canonicalCheck("kind = 'password' AND (state IS NULL OR archived_at IS NULL)", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		"(kind = 'password' AND state IS NULL) OR archived_at IS NULL",
		"kind = 'PASSWORD' AND (state IS NULL OR archived_at IS NULL)",
		"kind = 'password' AND (state IS NULL OR archived_at IS NULL) OR true",
	} {
		got, err := canonicalCheck(changed, "postgres")
		if err == nil && want == got {
			t.Fatal("changed grouping or literal was accepted", changed)
		}
	}
}
