package repair

import (
	"strings"
	"testing"
)

func TestTryRepairRevise_HappyPath(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后"}]}`
	revs, repaired, err := TryRepairRevise(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(revs) != 1 || revs[0].ID != "1" || revs[0].Target != "修订后" {
		t.Errorf("wrong: %#v", revs)
	}
	if len(repaired) != 0 {
		t.Errorf("unexpected repair: %v", repaired)
	}
}

func TestTryRepairRevise_NormalizesAndDeduplicates(t *testing.T) {
	in := `{"revisions":[{"id":" 1 ","target":" first "},{"id":"1","target":"second"},{"id":"2","target":""},{"id":"3","target":" third "}]}`
	revs, _, err := TryRepairRevise(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 2 || revs[0].ID != "1" || revs[0].Target != "first" || revs[1].ID != "3" {
		t.Fatalf("wrong: %#v", revs)
	}
}

func TestTryRepairRevise_TruncatedAndTrailingComma(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后",}]`
	revs, repaired, err := TryRepairRevise(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(revs) != 1 || revs[0].ID != "1" {
		t.Errorf("wrong: %#v", revs)
	}
	if len(repaired) == 0 {
		t.Errorf("expected repair ops")
	}
}

func TestTryRepairRevise_CodeFence(t *testing.T) {
	in := "```json\n{\"revisions\":[{\"id\":\"1\",\"target\":\"修订后\"}]}\n```"
	revs, _, err := TryRepairRevise(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].ID != "1" {
		t.Errorf("wrong: %#v", revs)
	}
}

func TestTryRepairRevise_FatalNotJSON(t *testing.T) {
	_, _, err := TryRepairRevise("totally not json", allOpts)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no JSON object") {
		t.Errorf("wrong err: %v", err)
	}
}

func TestParseReviseByMode_NonTextUsesJSONRepair(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后",}]}`
	revs, repaired, err := ParseReviseByMode(in, false, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || len(repaired) == 0 {
		t.Fatalf("revs=%#v repaired=%v", revs, repaired)
	}
}

func TestParseReviseByMode_TextRevision(t *testing.T) {
	in := "[revisions]\n1 | 修订后 | 含竖线\n"
	revs, repaired, err := ParseReviseByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].Target != "修订后 | 含竖线" {
		t.Errorf("wrong: %#v", revs)
	}
	if len(repaired) != 0 {
		t.Errorf("text path should not report repair ops: %v", repaired)
	}
}

func TestParseReviseByMode_TextJSONFallsBack(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后"}]}`
	revs, _, err := ParseReviseByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].ID != "1" {
		t.Errorf("wrong: %#v", revs)
	}
}
