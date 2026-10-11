package repair

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func TestLegacyAlignmentStateRejectsMalformedDuplicateGroup(t *testing.T) {
	entries, _, err := TryRepairRubyAlignmentForState(`{"ruby_output":[{"id":"6","base":"alpha","text":"a","kind":"creative"},{"id":"6","base":false,"text":"a","kind":"creative"},{"id":"9","base":"beta","text":"b","kind":"creative"}]}`, allOpts)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ruby.NewAlignmentState("alpha beta", "text", []ruby.Item{{ID: "6"}, {ID: "9"}}, nil, ruby.ProtocolLegacy)
	if err != nil {
		t.Fatal(err)
	}
	result := state.ApplyOutput([]string{"6", "9"}, entries, false)
	if result.Added != 1 || result.Rejected["6"] != "duplicate_id" || len(state.Missing()) != 1 || state.Missing()[0].ID != "6" {
		t.Fatalf("legacy partial facts not conserved: %+v %+v", result, state)
	}
}
