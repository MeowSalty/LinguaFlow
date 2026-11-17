package pipeline

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"reflect"
	"testing"
)

func TestMainPlannerLargeWindowUsesActualMembers(t *testing.T) {
	doc := &Document{Segments: []Segment{{Source: "two words"}}}
	b, err := backend.NewBuildBounds(1<<20, 1<<20, 128)
	if err != nil {
		t.Fatal(err)
	}
	h := &TranslateHandler{MaxWordsPerBatch: 4000}
	got, err := PlanNextMainBatchBounded(b, h, doc, func(yield func(int) bool) { yield(0) }, 0, 32768)
	if err != nil || !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("batch=%v err=%v", got, err)
	}
	if s := b.Snapshot(); s.MeasuredRequiredBytes > 128 || s.MeasuredRequiredBytes == 0 {
		t.Fatalf("unplanned capacity: %+v", s)
	}
}

func TestMainPlannerDefaultWindowPreservesDenseEPUBBatch(t *testing.T) {
	doc := &Document{Segments: make([]Segment, 256)}
	want := make([]int, len(doc.Segments))
	for i := range doc.Segments {
		doc.Segments[i].Source = "two words"
		want[i] = i
	}
	b, err := backend.NewBuildBounds(1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PlanNextMainBatchBounded(b, &TranslateHandler{MaxWordsPerBatch: 4000}, doc, func(yield func(int) bool) {
		for i := range doc.Segments {
			if !yield(i) {
				break
			}
		}
	}, 0, 256)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("members=%d err=%v", len(got), err)
	}
}
func TestMainPlannerStaticMemberPolicyRetainsNewPrefix(t *testing.T) {
	doc := &Document{Segments: make([]Segment, 257)}
	for i := range doc.Segments {
		doc.Segments[i].Source = "two words"
	}
	b, err := backend.NewBuildBounds(1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PlanNextMainBatchBounded(b, &TranslateHandler{MaxWordsPerBatch: 4000}, doc, func(yield func(int) bool) {
		for i := range doc.Segments {
			if !yield(i) {
				break
			}
		}
	}, 0, 256)
	if err != nil || len(got) != 256 {
		t.Fatalf("static prefix: members=%d err=%v", len(got), err)
	}
	for i, index := range got {
		if index != i {
			t.Fatalf("static prefix[%d]=%d", i, index)
		}
	}
}

func TestStoredPlannerDensePublicBatchIgnoresCandidateWindow(t *testing.T) {
	doc := &Document{Segments: make([]Segment, 3000)}
	for i := range doc.Segments {
		doc.Segments[i].Source = "two words"
		doc.Segments[i].RoundWork.Member = true
	}
	plan, err := planStoredCanonicalBatch(doc, &ExtractHandler{MaxWordsPerBatch: 4000}, 0, 1)
	if err != nil || plan.first != 0 || plan.last != 1999 || plan.members != 2000 {
		t.Fatalf("stored public batch changed: %+v err=%v", plan, err)
	}
}

func TestStoredPlannerPageRetainsReadyNaturalBatches(t *testing.T) {
	doc := &Document{Segments: make([]Segment, 2)}
	for i := range doc.Segments {
		doc.Segments[i].Source = "two words"
		doc.Segments[i].RoundWork.Member = true
	}
	b, _ := backend.NewBuildBounds(1, 1, 128)
	plans, members, err := planStoredCanonicalPage(b, doc, &ExtractHandler{BatchSize: 1}, 0, 1, 10)
	if err != nil || members != 2 || len(plans) != 2 || cap(plans) != 2 || plans[0].first != 0 || plans[1].first != 1 {
		t.Fatalf("ready page plans=%v members=%d err=%v", plans, members, err)
	}
	// A very large C must not allocate C descriptors for one natural batch.
	dense := &Document{Segments: make([]Segment, 2000)}
	for i := range dense.Segments {
		dense.Segments[i].Source = "two words"
		dense.Segments[i].RoundWork.Member = true
	}
	small, _ := backend.NewBuildBounds(1, 1, 128)
	plans, members, err = planStoredCanonicalPage(small, dense, &ExtractHandler{MaxWordsPerBatch: 4000}, 0, 1, 32768)
	if err != nil || members != 2000 || len(plans) != 1 || cap(plans) != 1 || small.Snapshot().LiveBytes > 128 {
		t.Fatalf("dense page plans=%v members=%d err=%v snapshot=%+v", plans, members, err, small.Snapshot())
	}
}
