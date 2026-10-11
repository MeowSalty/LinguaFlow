package ruby

import "testing"

func TestLegacyAlignmentNeverGuessesIdentityOrDropsMalformedDuplicates(t *testing.T) {
	entries := ParseAlignmentTextLegacy("alpha | a | creative | #6\n | a | creative | #6\nbeta | b | creative | #9\nalpha | a | creative")
	state, err := NewAlignmentState("alpha beta", "text", []Item{{ID: "6"}, {ID: "9"}}, nil, ProtocolLegacy)
	if err != nil {
		t.Fatal(err)
	}
	p := state.ApplyOutput([]string{"6", "9"}, entries, false)
	if p.Added != 1 || p.Rejected["6"] != "duplicate_id" || len(state.Verified) != 1 || state.Verified[0].ID != "9" {
		t.Fatalf("legacy parser manufactured facts: %+v %+v", entries, p)
	}
}

func TestLegacyAlignmentSchemaDoesNotDemandNewProtocolFields(t *testing.T) {
	for _, protocol := range []int{ProtocolLegacy, ProtocolV2} {
		schema := AlignmentJSONSchemaForProtocol(protocol)
		items := schema["properties"].(map[string]any)["ruby_output"].(map[string]any)["items"].(map[string]any)
		required := map[string]bool{}
		for _, key := range items["required"].([]string) {
			required[key] = true
		}
		if required["occurrence"] != (protocol == ProtocolV2) || required["id"] != (protocol == ProtocolV2) {
			t.Fatalf("protocol=%d required=%v", protocol, required)
		}
	}
}
