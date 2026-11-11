package pipeline

import (
	"context"
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

func TestCanonicalCommitPreservesParserTypesAndOpaqueMetadata(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	defer runtime.Close()
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	}}
	doc := newTestDoc(1)
	doc.Segments[0].Meta = map[string]any{
		"pos_lines": []int{1, 2}, "line": 7, "offset": int64(19),
		"extension": map[string]any{"label": "opaque", "flags": []bool{true, false}},
	}
	before, err := json.Marshal(doc.Segments[0].Meta["extension"])
	if err != nil {
		t.Fatal(err)
	}
	h := stagedTestHandler(t, runtime, inner)
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h}, doc, NewMemoryRoundStore(nil), runtime, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 1 || doc.Segments[0].Target != "translated" {
		t.Fatalf("confirmed metadata result=%+v target=%q err=%v", result, doc.Segments[0].Target, err)
	}
	meta := doc.Segments[0].Meta
	if !reflect.DeepEqual(meta["pos_lines"], []int{1, 2}) || meta["line"] != 7 || meta["offset"] != int64(19) {
		t.Fatalf("parser-native types changed: %+v", meta)
	}
	after, err := json.Marshal(meta["extension"])
	if err != nil || string(before) != string(after) {
		t.Fatalf("opaque metadata changed: before=%s after=%s err=%v", before, after, err)
	}
}

type originalSegmentFootprint struct {
	DBID                               int
	ContentVersion                     int64
	TargetIsNull                       bool
	ID, Source, OriginalSource, Target string
	Protected                          map[string]string
	Meta                               map[string]any
	Skip, Translate, StructuralOnly    bool
	Issues                             []qa.QualityIssue
	Status                             string
}

func TestStoredPageMetadataAllocationCoefficient(t *testing.T) {
	// Both result-channel and capacityWaiting backings may have allocated 2C
	// slots even though the source credits permit only C live descriptors.
	perWorker := 4*unsafe.Sizeof(batchResult{}) + 3*unsafe.Sizeof(batchJob{}) + 4*unsafe.Sizeof([]int{})
	t.Logf("stored_metadata_credit=512*N+2048*min(C,N); measured_channel_worker_records=%d; cursors remain canonical, no page map", perWorker)
	if perWorker > 2048 {
		t.Fatal("stored worker record proof needs a revised coefficient")
	}
}

func TestCanonicalStoredResultOutputBackingIsSeparate(t *testing.T) {
	for _, n := range []int{1, 3, 7, 31, 127, 257, 1025, 8192} {
		for _, split := range []bool{false, true} {
			doc := &Document{Segments: make([]Segment, n)}
			for i := range doc.Segments {
				w := &doc.Segments[i].RoundWork
				w.Member = true
				if !split || i%3 == 0 {
					w.Completed = true
				} else if i%3 == 1 {
					w.Cursor.Phase = "terminal_failure"
				}
			}
			r := canonicalStoredResult(doc, 0)
			count := len(r.Resolved) + len(r.Unresolved) + len(r.FailedSegments)
			backing := (cap(r.Resolved) + cap(r.Unresolved) + cap(r.FailedSegments)) * int(unsafe.Sizeof(int(0)))
			if count != n || backing > 16*n {
				t.Fatalf("output index ownership: n=%d split=%v count=%d capacity_bytes=%d", n, split, count, backing)
			}
			if n == 8192 {
				t.Logf("Delta_result_output n=%d split=%v actual_backing=%d bound=%d fixed_slice_headers=%d", n, split, backing, 16*n, 3*unsafe.Sizeof([]int{}))
			}
		}
	}
}

func TestCanonicalSnapshotEmptyIndicesRejectBeforeConstruction(t *testing.T) {
	if _, err := snapshotMainInput(nil, nil, nil, nil); err != ErrCandidateCapacity {
		t.Fatalf("empty input constructed unclaimed fixed objects: %v", err)
	}
}

func TestCanonicalDocumentFixedMetadataFootprint(t *testing.T) {
	delta := int64(unsafe.Sizeof(Segment{})) - int64(unsafe.Sizeof(originalSegmentFootprint{}))
	t.Logf("M_document_fixed_per_segment=%d original_segment=%d canonical_segment=%d work_record=%d; DB cursor scalar strings preguard <=128 bytes combined", delta, unsafe.Sizeof(originalSegmentFootprint{}), unsafe.Sizeof(Segment{}), unsafe.Sizeof(Segment{}.RoundWork))
	if delta <= 0 || unsafe.Sizeof(Segment{}) > 512 {
		t.Fatal("fixed canonical record outgrew its preallocation proof")
	}
	seg := Segment{DBID: 7, DBIndex: 9, CompletionModes: 15, Source: "same source"}
	seg.RoundWork.Member = true
	seg.RoundWork.Cursor.State = "private cursor marker"
	bytes, err := json.Marshal(seg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), "RoundWork") || strings.Contains(string(bytes), "private cursor marker") || strings.Contains(string(bytes), "CompletionModes") || strings.Contains(string(bytes), "DBIndex") {
		t.Fatalf("canonical state leaked into DTO %s", bytes)
	}
}
