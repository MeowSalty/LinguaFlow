package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// These storage-fault tests require every member to belong to the first
// auxiliary attempt. A batch deadline can legally flush partial promotions;
// a preparation barrier fixes the cohort without changing that deadline.
func captureRubyBatchCohort(t *testing.T, f *rubyPipelineFixture) map[string]workstate.RequestMember {
	t.Helper()
	prepareRubyBatchCandidates(t, f)
	ctx := context.Background()
	cohort := make(map[string]workstate.RequestMember)
	for _, row := range f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).AllX(ctx) {
		item := f.client.WorkItem.GetX(ctx, row.WorkItemID)
		id := workstate.WorkIdentity(workstate.Scope{JobID: f.jobID, RoundID: f.roundIDs[0]}, item.SegmentID)
		cohort[id] = workstate.RequestMember{SegmentID: item.SegmentID, WorkID: id, CandidateID: row.Identity, CandidateVersion: row.Version, Pool: item.PoolIndex}
	}
	if len(cohort) != 4 {
		t.Fatalf("first auxiliary cohort has %d members, want 4", len(cohort))
	}
	return cohort
}

func assertRubyBatchFirstCohort(t *testing.T, f *rubyPipelineFixture, cohort map[string]workstate.RequestMember, batchSize int) {
	t.Helper()
	requests := alignmentHTTPRequests(f)
	wantCalls := 4 / batchSize
	if len(requests) != wantCalls {
		t.Fatalf("first auxiliary cohort HTTP calls=%d, want %d: %+v", len(requests), wantCalls, requests)
	}
	if batchSize == 4 {
		seen := make(map[string]bool)
		for _, m := range requests[0].Alignments {
			want, ok := cohort[m.WorkID]
			if !ok || seen[m.WorkID] || m.CandidateID != want.CandidateID {
				t.Fatalf("HTTP alignment member outside first cohort: %+v", m)
			}
			seen[m.WorkID] = true
		}
		if len(seen) != 4 {
			t.Fatalf("HTTP alignment cohort=%+v, want all four members", requests[0].Alignments)
		}
	}
	// The single-member HTTP protocol has no member identity envelope. The
	// durable ledger proves exact identity and debits for both protocols.
	ctx := context.Background()
	rows := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID), workrequest.StageIn("alignment", "ruby_alignment"), workrequest.StateNEQ("cancelled_before_dispatch")).AllX(ctx)
	if len(rows) != wantCalls {
		t.Fatalf("first auxiliary ledger rows=%d, want %d", len(rows), wantCalls)
	}
	seen := make(map[string]bool)
	for _, row := range rows {
		var envelope struct {
			Version int                       `json:"version"`
			Members []workstate.RequestMember `json:"members"`
		}
		if err := json.Unmarshal(row.Members, &envelope); err != nil || envelope.Version != 1 || len(envelope.Members) != batchSize || len(row.SegmentIds) != batchSize {
			t.Fatalf("first auxiliary member ledger=%s, segments=%v, err=%v", row.Members, row.SegmentIds, err)
		}
		for i, member := range envelope.Members {
			want, ok := cohort[member.WorkID]
			if !ok || member != want || seen[member.WorkID] || member.SegmentID != row.SegmentIds[i] {
				t.Fatalf("first auxiliary ledger member=%+v, want=%+v, duplicate=%v", member, want, seen[member.WorkID])
			}
			seen[member.WorkID] = true
		}
	}
	if len(seen) != 4 {
		t.Fatalf("first auxiliary ledger debited %d of four members", len(seen))
	}
	t.Logf("first auxiliary cohort: HTTP=%d, ledger=%d, exact member identities/debits=4, batch=%d", len(requests), len(rows), batchSize)
}
