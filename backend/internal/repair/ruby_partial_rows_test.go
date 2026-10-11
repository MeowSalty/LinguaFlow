package repair

import "testing"

func TestOptionalRubyFailuresKeepMainResponseAndValidSibling(t *testing.T) {
	input := `{"translations":{"1":" a b "},"ruby_output":{"1":[{"id":"a","base":" a ","text":" x ","kind":"creative"},{"id":"b","base":"b","text":"y","kind":"phonetic","occurrence":"invalid"}]}}`
	result := TryRepair(input, []string{"1"}, allOpts)
	if result.Fatal || result.Trans["1"] != " a b " || len(result.RubyOutput["1"]) != 2 {
		t.Fatalf("main response lost: %+v", result)
	}
	entries := result.RubyOutput["1"]
	if entries[0].Base != " a " || entries[0].Text != " x " || entries[0].Invalid || !entries[1].Invalid || entries[1].ID != "b" {
		t.Fatalf("row isolation failed: %+v", entries)
	}
	revise := `{"revisions":[{"id":"1","target":"valid"}],"ruby_output":{"1":[{"id":"a","base":" a ","text":" x ","kind":"creative"},{"id":"b","base":"b","text":"y","kind":"phonetic","occurrence":"invalid"}]}}`
	revisions, outputs, _, err := ParseReviseByMode(revise, false, allOpts)
	if err != nil || len(revisions) != 1 || len(outputs["1"]) != 2 || outputs["1"][0].Invalid || !outputs["1"][1].Invalid {
		t.Fatalf("revision lost: %+v %+v %v", revisions, outputs, err)
	}
}
