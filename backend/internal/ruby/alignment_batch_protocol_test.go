package ruby

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAlignmentBatchRequestUsesSingleMemberViews(t *testing.T) {
	state := alignmentState(t, `<p>行&amp;行</p><ruby>旧<rt>old</rt></ruby>`, "html",
		Item{ID: "1", SourceBase: "行", SourceText: "xíng"},
		Item{ID: "2", SourceBase: "行", SourceText: "háng"})
	if progress := state.ApplyOutput([]string{"1"}, []OutputEntry{{ID: "1", Base: "行", Text: "xíng", Kind: "phonetic", Occurrence: 1}}, true); progress.Added != 1 {
		t.Fatal(progress)
	}
	members := []AlignmentBatchMember{
		{WorkID: "工作|一", CandidateID: "候选\"一", Source: `<ruby>行<rt>xíng</rt></ruby> 行`, Alignment: state},
		{WorkID: "work-2", CandidateID: "candidate-2", Source: "source", Alignment: alignmentState(t, "訳文", "text", Item{ID: "1", SourceBase: "原文", SourceText: "よみ"})},
	}
	request, err := AlignmentBatchRequest(members)
	if err != nil {
		t.Fatal(err)
	}
	var batch struct {
		Alignments []map[string]any `json:"alignments"`
	}
	if err := json.Unmarshal([]byte(request), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Alignments) != len(members) {
		t.Fatal(batch)
	}
	for i, member := range members {
		actual := batch.Alignments[i]
		if actual["work_id"] != member.WorkID || actual["candidate_id"] != member.CandidateID {
			t.Fatalf("identity changed: %+v", actual)
		}
		delete(actual, "work_id")
		delete(actual, "candidate_id")
		var single map[string]any
		if err := json.Unmarshal([]byte(member.Alignment.AlignmentRequest(member.Source, member.Alignment.Missing())), &single); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, single) {
			t.Fatalf("batch/single public views diverged: %+v != %+v", actual, single)
		}
	}
	if missing := batch.Alignments[0]["missing"].([]any); len(missing) != 1 || missing[0].(map[string]any)["id"] != "2" {
		t.Fatalf("verified item resent: %+v", missing)
	}
	regions := batch.Alignments[0]["translation_regions"].([]any)
	if len(regions) != 1 || !reflect.DeepEqual(regions[0], map[string]any{"text": "行&行"}) {
		t.Fatalf("private coordinates or unsafe regions exposed: %+v", regions)
	}
}

func TestAlignmentRequestTextsTracksRepeatedContentAndMissing(t *testing.T) {
	s := alignmentState(t, `<p>行&行</p><b>go now</b>`, "html",
		Item{ID: "metadata-1", SourceBase: "行", SourceText: "xíng"},
		Item{ID: "metadata-2", SourceBase: "go", SourceText: "前进"})
	source := `<ruby>行<rt>xíng</rt></ruby> 原文`
	want := []string{"行 原文", s.Translation, "行&行", "go now", "行", "xíng", "go", "前进"}
	if got := s.AlignmentRequestTexts(source, s.Missing()); !reflect.DeepEqual(got, want) {
		t.Fatalf("content count fields = %q, want %q", got, want)
	}
	if progress := s.ApplyOutput([]string{"metadata-2"}, []OutputEntry{{ID: "metadata-2", Base: "go", Text: "前进", Kind: "creative", Occurrence: 1}}, true); progress.Added != 1 {
		t.Fatal(progress)
	}
	if got := s.AlignmentRequestTexts(source, s.Missing()); !reflect.DeepEqual(got, want[:6]) {
		t.Fatalf("completed missing entry still counted: %q", got)
	}
}

func TestAlignmentBatchRequestRejectsAmbiguousInputs(t *testing.T) {
	valid := AlignmentBatchMember{WorkID: "w", CandidateID: "c", Alignment: alignmentState(t, "text", "text", Item{ID: "1"})}
	legacy, err := NewAlignmentState("legacy", "text", nil, nil, ProtocolLegacy)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := *valid.Alignment
	corrupt.Translation = "changed"
	for name, members := range map[string][]AlignmentBatchMember{
		"empty":             nil,
		"duplicate":         {valid, valid},
		"missing_work":      {{CandidateID: "c", Alignment: valid.Alignment}},
		"missing_candidate": {{WorkID: "w", Alignment: valid.Alignment}},
		"invalid_utf8":      {{WorkID: "w\xff", CandidateID: "c", Alignment: valid.Alignment}},
		"nil_state":         {{WorkID: "w", CandidateID: "c"}},
		"corrupt_state":     {{WorkID: "w", CandidateID: "c", Alignment: &corrupt}},
		"legacy_state":      {{WorkID: "w", CandidateID: "c", Alignment: legacy}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := AlignmentBatchRequest(members); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

const batchTestRow = `{"id":"1","base":"行","text":"xíng","kind":"phonetic","occurrence":1}`

func batchJSONTestMember(work, candidate, rows string) string {
	w, _ := json.Marshal(work)
	c, _ := json.Marshal(candidate)
	return `{"work_id":` + string(w) + `,"candidate_id":` + string(c) + `,"ruby_output":` + rows + `}`
}

func TestAlignmentBatchJSONAssociatesIdentityAndKeepsEmptyUnknownMembers(t *testing.T) {
	response := `{"alignments":[` + strings.Join([]string{
		batchJSONTestMember("w2", "c2", `[]`),
		batchJSONTestMember("unknown", "extra", `[]`),
		batchJSONTestMember("w1", "c1", `[`+batchTestRow+`]`),
	}, ",") + `]}`
	result := ParseAlignmentBatchJSON(response)
	if result.EnvelopeInvalid || len(result.Invalid) != 0 || len(result.Entries) != 3 {
		t.Fatalf("valid members rejected: %+v", result)
	}
	if result.Entries[AlignmentBatchKey{"w1", "c1"}][0].Base != "行" {
		t.Fatal(result)
	}
	if entries, exists := result.Entries[AlignmentBatchKey{"w2", "c2"}]; !exists || len(entries) != 0 {
		t.Fatal("empty result is indistinguishable from missing member")
	}
}

func TestAlignmentBatchJSONMemberFailuresAreIsolated(t *testing.T) {
	valid := batchJSONTestMember("w1", "c1", `[`+batchTestRow+`]`)
	for name, invalid := range map[string]string{
		"missing_output":   `{"work_id":"w2","candidate_id":"c2"}`,
		"null_output":      batchJSONTestMember("w2", "c2", `null`),
		"object_output":    batchJSONTestMember("w2", "c2", `{}`),
		"extra_field":      `{"work_id":"w2","candidate_id":"c2","ruby_output":[],"extra":1}`,
		"duplicate_output": `{"work_id":"w2","candidate_id":"c2","ruby_output":[],"ruby_output":[]}`,
		"duplicate_work":   `{"work_id":"w2","work_id":"w2","candidate_id":"c2","ruby_output":[]}`,
		"duplicate_member": batchJSONTestMember("w2", "c2", `[]`) + `,` + batchJSONTestMember("w2", "c2", `[]`),
	} {
		t.Run(name, func(t *testing.T) {
			result := ParseAlignmentBatchJSON(`{"alignments":[` + valid + `,` + invalid + `]}`)
			if len(result.Entries) != 1 || len(result.Entries[AlignmentBatchKey{"w1", "c1"}]) != 1 || !result.Invalid[AlignmentBatchKey{"w2", "c2"}] {
				t.Fatalf("member failure affected unrelated output: %+v", result)
			}
		})
	}
}

func TestAlignmentBatchJSONIdentityConflictsCannotHideDuplicates(t *testing.T) {
	for _, conflictFirst := range []bool{false, true} {
		members := []string{
			batchJSONTestMember("w1", "c1", `[]`),
			batchJSONTestMember("w2", "c2", `[]`),
			batchJSONTestMember("safe", "safe", `[]`),
		}
		conflict := `{"work_id":"w1","work_id":"w2","candidate_id":"c1","candidate_id":"c2","ruby_output":[]}`
		if conflictFirst {
			members = append([]string{conflict}, members...)
		} else {
			members = append(members, conflict)
		}
		result := ParseAlignmentBatchJSON(`{"alignments":[` + strings.Join(members, ",") + `]}`)
		if !result.EnvelopeInvalid || !result.Invalid[AlignmentBatchKey{"w1", "c1"}] || !result.Invalid[AlignmentBatchKey{"w2", "c2"}] || len(result.Entries) != 1 {
			t.Fatalf("conflicting identities accepted: %+v", result)
		}
	}
}

func TestAlignmentBatchJSONMissingIdentityDoesNotGuessAssociation(t *testing.T) {
	for _, member := range []string{
		`{"candidate_id":"c1","ruby_output":[]}`,
		`{"work_id":"w1","ruby_output":[]}`,
		`{"work_id":"","candidate_id":"c1","ruby_output":[]}`,
		`{"work_id":null,"candidate_id":"c1","ruby_output":[]}`,
		`{"work_id":1,"candidate_id":"c1","ruby_output":[]}`,
		`null`,
	} {
		result := ParseAlignmentBatchJSON(`{"alignments":[` + member + `,` + batchJSONTestMember("w2", "c2", `[]`) + `]}`)
		if !result.EnvelopeInvalid || len(result.Entries) != 1 {
			t.Fatalf("unidentified member guessed or safe member lost: %+v", result)
		}
	}
}

func TestAlignmentBatchJSONMalformedRowsRemainIndividualFailures(t *testing.T) {
	for name, row := range map[string]string{
		"null":               `null`,
		"missing_id":         `{"base":"行","text":"xíng","kind":"phonetic","occurrence":1}`,
		"duplicate_id_field": `{"id":"1","id":"1","base":"行","text":"xíng","kind":"phonetic","occurrence":1}`,
		"wrong_occurrence":   `{"id":"1","base":"行","text":"xíng","kind":"phonetic","occurrence":0}`,
		"wrong_kind":         `{"id":"1","base":"行","text":"xíng","kind":"unknown","occurrence":1}`,
		"extra_field":        `{"id":"1","base":"行","text":"xíng","kind":"phonetic","occurrence":1,"offset":1}`,
		"invalid_utf8":       "{\"id\":\"1\",\"base\":\"\xff\",\"text\":\"x\",\"kind\":\"phonetic\",\"occurrence\":1}",
	} {
		t.Run(name, func(t *testing.T) {
			result := ParseAlignmentBatchJSON(`{"alignments":[` + batchJSONTestMember("w", "c", `[`+row+`,`+batchTestRow+`]`) + `]}`)
			entries := result.Entries[AlignmentBatchKey{"w", "c"}]
			if result.EnvelopeInvalid || len(result.Invalid) != 0 || len(entries) != 2 || !entries[0].Invalid || entries[1].Invalid {
				t.Fatalf("row error became a batch/member failure: %+v", result)
			}
			if entries[0].ID == "1" {
				s := alignmentState(t, "行", "text", Item{ID: "1"})
				if progress := s.ApplyOutput([]string{"1"}, entries, true); progress.Added != 0 || progress.Rejected["1"] != "duplicate_id" {
					t.Fatalf("invalid row hid duplicate ID: %+v", progress)
				}
			}
		})
	}
}

func TestAlignmentBatchJSONTruncationKeepsOnlyCompleteMembers(t *testing.T) {
	first := batchJSONTestMember("w1", "c1", `[`+batchTestRow+`]`)
	second := batchJSONTestMember("w2", "c2", `[`+batchTestRow+`]`)
	prefix := `{"alignments":[` + first
	full := prefix + `,` + second + `]}`
	for cut := 0; cut < len(full); cut++ {
		result := ParseAlignmentBatchJSON(full[:cut])
		if !result.EnvelopeInvalid {
			t.Fatalf("cut %d: truncated envelope accepted", cut)
		}
		_, gotFirst := result.Entries[AlignmentBatchKey{"w1", "c1"}]
		_, gotSecond := result.Entries[AlignmentBatchKey{"w2", "c2"}]
		if gotFirst != (cut >= len(prefix)) || gotSecond != (cut >= len(full)-2) {
			t.Fatalf("cut %d: repaired a partial member or lost a complete member: %+v", cut, result)
		}
	}
}

func TestAlignmentBatchJSONTruncatedDuplicateCannotHideIdentity(t *testing.T) {
	first := batchJSONTestMember("w1", "c1", `[`+batchTestRow+`]`)
	safe := batchJSONTestMember("safe", "safe", `[]`)
	for _, tail := range []string{
		`{"work_id":"w1","candidate_id":"c1","ruby_output":[`,
		`{"candidate_id":"c1","work_id":"w1","ruby_output":[{"id":"1"`,
		`{"work_id":"w1","candidate_id":"c1"`,
	} {
		result := ParseAlignmentBatchJSON(`{"alignments":[` + first + `,` + safe + `,` + tail)
		if !result.EnvelopeInvalid || !result.Invalid[AlignmentBatchKey{"w1", "c1"}] || len(result.Entries) != 1 {
			t.Fatalf("truncated duplicate manufactured unique association: %+v", result)
		}
	}
}

func TestAlignmentBatchJSONEnvelopeDamagePreservesSafeMembers(t *testing.T) {
	member := batchJSONTestMember("w", "c", `[]`)
	for name, input := range map[string]string{
		"unexpected_field": `{"metadata":true,"alignments":[` + member + `]}`,
		"trailing_data":    `{"alignments":[` + member + `]} garbage`,
		"missing_comma":    `{"alignments":[` + member + ` {]}`,
	} {
		t.Run(name, func(t *testing.T) {
			result := ParseAlignmentBatchJSON(input)
			if !result.EnvelopeInvalid || len(result.Entries) != 1 {
				t.Fatalf("safe member lost: %+v", result)
			}
		})
	}
	for _, input := range []string{"", `[]`, `null`, `{}`, `{"alignments":null}`, `{"alignments":{}}`, "```json\n{\"alignments\":[]}\n```"} {
		result := ParseAlignmentBatchJSON(input)
		if !result.EnvelopeInvalid || len(result.Entries) != 0 {
			t.Fatalf("invalid envelope repaired: %q %+v", input, result)
		}
	}
}

func TestAlignmentBatchTextGroupsMappingsAndEmptyMembers(t *testing.T) {
	input := strings.Join([]string{
		`"w2" | "c2" | "" | "" | "" | "" | 0`,
		`"w1" | "c1" | "行" | "xíng" | "phonetic" | "1" | 1`,
		`"extra" | "extra" | "" | "" | "" | "" | 0`,
		`"w1" | "c1" | "行" | "háng" | "phonetic" | "2" | 2`,
	}, "\n")
	result := ParseAlignmentBatchText(input)
	if result.EnvelopeInvalid || len(result.Invalid) != 0 || len(result.Entries) != 3 || len(result.Entries[AlignmentBatchKey{"w1", "c1"}]) != 2 {
		t.Fatalf("normal repeated member rows rejected: %+v", result)
	}
	if entries, exists := result.Entries[AlignmentBatchKey{"w2", "c2"}]; !exists || len(entries) != 0 {
		t.Fatal("empty marker lost")
	}
	s := alignmentState(t, "行行", "text", Item{ID: "1"}, Item{ID: "2"})
	if progress := s.ApplyOutput([]string{"1", "2"}, result.Entries[AlignmentBatchKey{"w1", "c1"}], true); progress.Added != 2 {
		t.Fatalf("batch changed occurrence validation: %+v", progress)
	}
}

func TestAlignmentBatchTextEmptyMarkerConflictsAreIsolated(t *testing.T) {
	marker := `"w1" | "c1" | "" | "" | "" | "" | 0`
	row := `"w1" | "c1" | "行" | "xíng" | "phonetic" | "1" | 1`
	for name, lines := range map[string][]string{
		"duplicate_marker": {marker, marker},
		"marker_then_row":  {marker, row},
		"row_then_marker":  {row, marker},
	} {
		t.Run(name, func(t *testing.T) {
			lines = append(lines, `"w2" | "c2" | "" | "" | "" | "" | 0`)
			result := ParseAlignmentBatchText(strings.Join(lines, "\n"))
			if !result.Invalid[AlignmentBatchKey{"w1", "c1"}] || len(result.Entries) != 1 {
				t.Fatalf("empty marker conflict not isolated: %+v", result)
			}
		})
	}
}

func TestAlignmentBatchTextEscapesAndWhitespaceAreExact(t *testing.T) {
	key := AlignmentBatchKey{"工作|\"一\n", "候选\\二"}
	want := OutputEntry{ID: "項|一", Base: " A|B\n\" ", Text: " é\\读音 ", Kind: "creative", Occurrence: 2}
	fields := []string{key.WorkID, key.CandidateID, want.Base, want.Text, want.Kind, want.ID}
	for i, field := range fields {
		encoded, err := json.Marshal(field)
		if err != nil {
			t.Fatal(err)
		}
		fields[i] = string(encoded)
	}
	input := " \t" + strings.Join(fields, " | ") + " | 2\r\n"
	for _, result := range []AlignmentBatchResult{
		ParseAlignmentBatchText(input),
		ParseAlignmentBatchJSON(`{"alignments":[` + batchJSONTestMember(key.WorkID, key.CandidateID, `[{"id":"項|一","base":" A|B\n\" ","text":" é\\读音 ","kind":"creative","occurrence":2}]`) + `]}`),
	} {
		if result.EnvelopeInvalid || len(result.Invalid) != 0 || !reflect.DeepEqual(result.Entries[key], []OutputEntry{want}) {
			t.Fatalf("decoded strings changed: %+v", result)
		}
	}
}

func TestAlignmentBatchTextMalformedRowsPreserveKnownIDs(t *testing.T) {
	for name, row := range map[string]string{
		"bad_base_escape":    `"行\q" | "x" | "phonetic" | "1" | 1`,
		"wrong_occurrence":   `"行" | "x" | "phonetic" | "1" | 0`,
		"missing_occurrence": `"行" | "x" | "phonetic" | "1"`,
		"extra_field":        `"行" | "x" | "phonetic" | "1" | 1 | extra`,
		"invalid_utf8":       "\"行\xff\" | \"x\" | \"phonetic\" | \"1\" | 1",
	} {
		t.Run(name, func(t *testing.T) {
			input := `"w" | "c" | ` + row + "\n" + `"w" | "c" | "行" | "x" | "phonetic" | "1" | 1`
			result := ParseAlignmentBatchText(input)
			entries := result.Entries[AlignmentBatchKey{"w", "c"}]
			if result.EnvelopeInvalid || len(result.Invalid) != 0 || len(entries) != 2 || !entries[0].Invalid || entries[0].ID != "1" || entries[1].Invalid {
				t.Fatalf("malformed item became member failure or hid its ID: %+v", result)
			}
			s := alignmentState(t, "行", "text", Item{ID: "1"})
			if progress := s.ApplyOutput([]string{"1"}, entries, true); progress.Added != 0 || progress.Rejected["1"] != "duplicate_id" {
				t.Fatalf("duplicate ID became trusted: %+v", progress)
			}
		})
	}
}

func TestAlignmentBatchTextMissingIdentityKeepsOtherMembers(t *testing.T) {
	for _, row := range []string{
		`"w"`,
		`"" | "c" | "行" | "x" | "phonetic" | "1" | 1`,
		`null | "c" | "行" | "x" | "phonetic" | "1" | 1`,
		`"w" | "unterminated`,
		`"w\q" | "c" | "行" | "x" | "phonetic" | "1" | 1`,
	} {
		result := ParseAlignmentBatchText(`"safe" | "safe" | "" | "" | "" | "" | 0` + "\n" + row)
		if !result.EnvelopeInvalid || len(result.Entries) != 1 {
			t.Fatalf("member identity was guessed or safe row lost: %+v", result)
		}
	}
}

func TestAlignmentBatchSchemaReusesV2Rows(t *testing.T) {
	schema := AlignmentBatchJSONSchema()
	member := schema["properties"].(map[string]any)["alignments"].(map[string]any)["items"].(map[string]any)
	if !reflect.DeepEqual(member["required"], []string{"work_id", "candidate_id", "ruby_output"}) || member["additionalProperties"] != false {
		t.Fatal(member)
	}
	output := member["properties"].(map[string]any)["ruby_output"]
	if !reflect.DeepEqual(output, AlignmentJSONSchema()["properties"].(map[string]any)["ruby_output"]) {
		t.Fatal("batch schema diverged from v2 mappings")
	}
}
