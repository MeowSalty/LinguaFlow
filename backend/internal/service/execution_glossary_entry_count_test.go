package service

import (
	"context"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

func TestResponseGlossaryEntryCountHasNoGraphQuota(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		count int
		valid bool
	}{
		{"null", 0, true}, {" [ ] ", 0, true},
		{`[{"source":"a,]"},[1,2],null]`, 3, true},
		{"[{},]", 0, false}, {"[{} {}]", 0, false}, {"[{}]{}", 0, false},
		{"[" + strings.Repeat("{},", 65536) + "{}]", 65537, true},
	} {
		count, err := responseGlossaryEntryCount(context.Background(), glossary.ResponseProcess{EntriesPayload: []byte(tc.raw)})
		if (err == nil) != tc.valid || tc.valid && count != tc.count {
			t.Fatalf("count=%d err=%v expected=%d valid=%v", count, err, tc.count, tc.valid)
		}
	}
}
