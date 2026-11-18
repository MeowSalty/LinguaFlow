package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequestinputmember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// These bounds are fixed before running the matrix. Freeze, archive,
// publication, promotion and commit have fewer than 20 byte-processing passes;
// 32 allows their fixed protocol work. Small headers have a separate allowance.
const memberWorkBytePasses int64 = 32
const memberWorkMetadataBytes int64 = 4096
const memberWorkRowsPerMember int64 = 64

type memberWorkTotals struct {
	mu                                                     sync.Mutex
	bytes, rows, sourceScans, hotFullJob, startupFullJob   int64
	liveFrames, liveFrameBytes, peakFrames, peakFrameBytes int64
	peakRoot                                               int64
	started                                                bool
	invalid                                                bool
	operations                                             map[string]int64
	root                                                   *resourcebudget.RootBudget
	rootActor                                              int
}

func (m *memberWorkTotals) observe(e backend.MemberWorkEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Bytes < 0 || e.Rows < 0 {
		m.invalid = true
	}
	if m.operations == nil {
		m.operations = make(map[string]int64)
	}
	m.operations[e.Phase+"/"+e.Operation]++
	if e.Phase == "capture" || e.Phase == "prepare" || e.Phase == "member" {
		m.started = true
	}
	if e.FullJob {
		if m.started {
			m.hotFullJob++
		} else {
			m.startupFullJob++
		}
	}
	switch e.Operation {
	case "frame_acquire":
		m.liveFrames += e.Rows
		m.liveFrameBytes += e.Bytes
		m.peakFrames = max(m.peakFrames, m.liveFrames)
		m.peakFrameBytes = max(m.peakFrameBytes, m.liveFrameBytes)
	case "frame_release":
		m.liveFrames -= e.Rows
		m.liveFrameBytes -= e.Bytes
		if m.liveFrames < 0 || m.liveFrameBytes < 0 {
			m.invalid = true
		}
	default:
		m.bytes += e.Bytes
		m.rows += e.Rows
	}
	if e.Operation == "source_scan" {
		m.sourceScans += e.Bytes
	}
	if m.root != nil {
		m.peakRoot = max(m.peakRoot, m.root.Snapshot(m.rootActor).Instance.PayloadBytes)
	}
}

func (m *memberWorkTotals) check(t *testing.T, n int, payload, raw int64) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.invalid || m.hotFullJob != 0 {
		t.Fatalf("invalid event/whole-job query during hot work: invalid=%t hot=%d ops=%v", m.invalid, m.hotFullJob, m.operations)
	}
	byteBound := memberWorkBytePasses*payload + memberWorkMetadataBytes*int64(n)
	if m.bytes <= 0 || m.bytes > byteBound || m.rows > memberWorkRowsPerMember*int64(n)+128 {
		t.Fatalf("work grew beyond fixed stage bounds: n=%d bytes=%d/%d rows=%d/%d ops=%v", n, m.bytes, byteBound, m.rows, memberWorkRowsPerMember*int64(n)+128, m.operations)
	}
	if m.sourceScans <= 0 || m.sourceScans > 2*raw {
		t.Fatalf("archive source scans=%d raw=%d", m.sourceScans, raw)
	}
	// Two promotions can hold one input and one output each; one frame can
	// overlap release bookkeeping. Archive's per-ID input borrows end immediately.
	if m.peakFrames == 0 || m.peakFrames > 5 || m.liveFrames != 0 || m.liveFrameBytes != 0 {
		t.Fatalf("member frame lifetime: peak=%d live=%d bytes=%d ops=%v", m.peakFrames, m.liveFrames, m.liveFrameBytes, m.operations)
	}
	// Root also includes the existing finite R/progress reservation. Its sample
	// is diagnostic; the actual backing peak has the payload bound below.
	if m.peakRoot == 0 {
		t.Fatal("Root was never observed during the real member path")
	}
	for _, operation := range []string{"output_write", "checkpoint_write", "business_write", "promotion_write"} {
		if m.operations["member/"+operation] == 0 {
			t.Fatalf("missing real mutation %s: %v", operation, m.operations)
		}
	}
	var coverage = map[string]bool{}
	for key := range m.operations {
		for _, op := range []string{"read", "hash", "decode"} {
			if strings.HasSuffix(key, "/"+op) || strings.HasSuffix(key, "_"+op) {
				coverage[op] = true
			}
		}
	}
	for _, op := range []string{"read", "hash", "decode"} {
		if !coverage[op] {
			t.Fatalf("missing real %s coverage: %v", op, m.operations)
		}
	}
	t.Logf("N=%d work_bytes=%d bound=%d rows=%d startup_full_job=%d hot_full_job=%d source_scans=%d raw=%d peak_frames=%d peak_frame_bytes=%d peak_root=%d operations=%v", n, m.bytes, byteBound, m.rows, m.startupFullJob, m.hotFullJob, m.sourceScans, raw, m.peakFrames, m.peakFrameBytes, m.peakRoot, m.operations)
}

func TestMemberResponseWorkSQLite(t *testing.T) {
	for _, n := range []int{32, 64, 128, 256} {
		t.Run(fmt.Sprintf("N%d", n), func(t *testing.T) {
			ctx := t.Context()
			f := newRubyPipelineFixtureWithClient(t, allocationSQLite(t), "", 0)
			sourceText := strings.TrimSpace(strings.Repeat(strings.Repeat("s", 72)+" ", 14))
			target := strings.Repeat("translation", 96)
			resourceID := f.resourceIDs[0]
			f.client.Resource.UpdateOneID(resourceID).SetFormat("text").SetTotalSegments(n).ExecX(ctx)
			f.client.JobResource.UpdateOneID(f.jobResources[0]).SetStatus(service.JobResourceStatusPending).SetSegmentCount(n).ExecX(ctx)
			for start := 0; start < n; start += 100 {
				builders := make([]*ent.SegmentCreate, 0, min(100, n-start))
				for i := start; i < min(n, start+100); i++ {
					builders = append(builders, f.client.Segment.Create().SetResourceID(resourceID).SetSegmentIndex(i).SetSourceText(sourceText))
				}
				f.client.Segment.CreateBulk(builders...).ExecX(ctx)
			}
			tr := f.snapshot.Rounds[0].Translate
			tr.BatchSize, tr.MaxWordsPerBatch, tr.Concurrency, tr.Retry.MaxAttempts = 0, 4000, 1, 0
			f.snapshot.Strategy.Ruby.Enabled = false
			f.snapshot.Strategy.Context.Enabled = false
			f.snapshot.RubyRetry = nil
			var err error
			f.snapshot, err = execution.Resolve(*f.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			f.persistSnapshot(t)
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			inventory := workstate.InventorySource{DB: f.client, Driver: "sqlite", ItemBytes: f.runner.pipeCfg.Candidates.ItemBytes}
			f.runner.SetRootBudget(root, nil)
			f.runner.SetBudgetSource(inventory, nil)
			workstate.InstallStorageHooks(f.client)
			observed := &allocationHTTPObserver{}
			clients := telemetry.NewHTTPClients(nil, observed)
			t.Cleanup(clients.Shutdown)
			f.runner.httpClients = []telemetry.HTTPClientFactory{clients}
			totals := &memberWorkTotals{root: root, rootActor: f.ownerID}
			var rawBytes int64
			f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
				translations := make(map[string]string, n)
				for id, s := range request.Segments {
					if s.Translate {
						translations[id] = target
					}
				}
				if len(translations) != n || len(request.Alignments) != 0 || len(request.Missing) != 0 {
					return nil, fmt.Errorf("planner boundary changed: members=%d want=%d", len(translations), n)
				}
				body := map[string]any{"translations": translations}
				encoded, err := json.Marshal(body)
				f.mu.Lock()
				rawBytes = int64(len(encoded))
				f.mu.Unlock()
				return body, err
			}
			recorded := backend.WithMemberWorkRecorder(ctx, totals.observe)
			if err = f.runner.processJob(recorded, f.jobID); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			calls := len(f.requests)
			rawTotal := rawBytes
			f.mu.Unlock()
			if calls != 1 || f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusCompleted {
				failed := f.client.Job.GetX(ctx, f.jobID)
				var cause string
				if failed.ErrorMessage != nil {
					cause = *failed.ErrorMessage
				}
				for _, roundID := range f.roundIDs {
					round := f.client.JobRound.GetX(ctx, roundID)
					if round.ErrorMessage != nil {
						t.Logf("round=%d cause=%s", roundID, *round.ErrorMessage)
					}
				}
				t.Logf("confirmed=%d candidates=%d translated=%d", f.client.JobRoundSegment.Query().CountX(ctx), f.client.WorkCandidate.Query().CountX(ctx), f.client.Segment.Query().Where(segment.ResourceIDEQ(resourceID), segment.StatusEQ(segment.StatusTranslated)).CountX(ctx))
				totals.mu.Lock()
				t.Logf("work_bytes=%d rows=%d operations=%v frames=%d", totals.bytes, totals.rows, totals.operations, totals.liveFrames)
				totals.mu.Unlock()
				for _, r := range f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).AllX(ctx) {
					t.Logf("request=%s format=%d phase=%s archive=%d/%d promotion=%d input_bytes=%d output_bytes=%d capture_bytes=%d", r.Identity, r.ResponseFormatVersion, r.PromotionState, r.ArchiveCursor, r.PromotionCount, r.PromotionCursor, r.InputMemberBytes, r.OutputMemberBytes, r.CaptureBytes)
				}
				t.Fatalf("calls=%d job=%s cause=%s", calls, failed.Status, cause)
			}
			f.assertUsage(t, 1)
			rows := f.client.Segment.Query().Where(segment.ResourceIDEQ(resourceID)).Order(ent.Asc(segment.FieldSegmentIndex)).AllX(ctx)
			if len(rows) != n {
				t.Fatalf("segment rows=%d want=%d", len(rows), n)
			}
			for _, row := range rows {
				if row.TargetText == nil || *row.TargetText != target || row.Status != segment.StatusTranslated || row.ContentVersion != 2 {
					t.Fatalf("member result/baseline changed: %+v", row)
				}
			}
			if confirmed := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(ctx); confirmed != n {
				t.Fatalf("confirmed=%d want=%d", confirmed, n)
			}
			requests := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID), workrequest.StateNEQ("cancelled_before_dispatch")).AllX(ctx)
			if len(requests) != 1 {
				t.Fatalf("fixed attempt requests=%d", len(requests))
			}
			r := requests[0]
			if r.PromotionCount != n || r.PromotionCursor != n || r.State != "completed" || r.InputMemberBytes+r.OutputMemberBytes+r.CaptureBytes != 0 {
				t.Fatalf("durable final cursor/inventory: %+v", r)
			}
			members := f.client.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID)).Order(ent.Asc(workrequestinputmember.FieldOrdinal)).AllX(ctx)
			if len(members) != n {
				t.Fatalf("durable members=%d want=%d", len(members), n)
			}
			for i, m := range members {
				if m.Ordinal != i || m.SegmentID != rows[i].ID || m.BaselineVersion != 1 || m.BaselineDigest == "" {
					t.Fatalf("member identity/baseline lost: %+v", m)
				}
			}
			totals.check(t, n, int64(n*(len(sourceText)+len(target)))+rawTotal, rawTotal)
			observed.mu.Lock()
			admission := observed.admission
			observed.mu.Unlock()
			if admission == nil {
				t.Fatal("HTTP ownership observer has no real admission")
			}
			owned := admission.OwnedPayloadSnapshot()
			if owned.TotalPeak <= 0 || owned.TotalPeak > memberWorkBytePasses*(int64(n*(len(sourceText)+len(target)))+rawTotal)+memberWorkMetadataBytes*int64(n) {
				t.Fatalf("actual backing peak exceeds fixed bound: %+v", owned)
			}
			if err := verifyHandoffOwnedTerminal(owned, "freeze", "raw", "wire", "candidate"); err != nil {
				t.Fatal(err)
			}
			t.Logf("actual_backing_peak=%d terminal_categories=%v", owned.TotalPeak, owned.Current)
			if totals.operations["settle/row_length"] == 0 {
				t.Fatal("SQLite touched-row physical-length query was not observed")
			}
			// This audit has its own context; it is not part of the hot-path collector.
			live, storage, err := inventory.Job(ctx, f.jobID)
			if err != nil {
				t.Fatal(err)
			}
			if rawStorage := auditMemberStorageSQL(t, ctx, f.client, f.jobID); rawStorage != storage {
				t.Fatalf("independent physical storage=%d inventory=%d", rawStorage, storage)
			}
			if live != (resourcebudget.Vector{}) {
				t.Fatalf("durable execution inventory remains: %+v", live)
			}
			live.LogicalStorageBytes = storage
			if got := root.Snapshot(f.ownerID).Instance; got != live {
				for _, identity := range []string{workstate.StorageIdentity(f.jobID), workstate.RuntimeIdentity(f.jobID)} {
					owned, attached := root.Usage(resourcebudget.Scope{ActorID: f.ownerID, JobID: f.jobID}, identity)
					t.Logf("identity=%s attached=%t usage=%+v", identity, attached, owned)
				}
				for _, request := range requests {
					t.Logf("request_metadata_bytes=%d member_storage_bytes=%d", request.RequestMetadataBytes, request.MemberStorageBytes)
					credit, attached := admission.StorageCredit(request.Identity)
					t.Logf("request=%s save_credit=%d attached=%t", request.Identity, credit, attached)
				}
				t.Fatalf("joined Root/durable baseline mismatch: root=%+v durable=%+v", got, live)
			}
			t.Logf("N=%d calls=1 final_execution=zero durable_storage=%d Root=%+v", n, storage, live)
		})
	}
}

// Deliberately independent of InventorySource's expressions: only SQL lengths
// of actual columns are counted, including immutable physical legacy backing.
func auditMemberStorageSQL(t *testing.T, ctx context.Context, client *ent.Client, jobID int) int64 {
	t.Helper()
	columns, err := client.QueryContext(ctx, "PRAGMA table_info(work_requests)")
	if err != nil {
		t.Errorf("raw audit columns: %v", err)
		return 0
	}
	present := map[string]bool{}
	for columns.Next() {
		var cid, notnull, primary int
		var name, typ string
		var value any
		if err = columns.Scan(&cid, &name, &typ, &notnull, &value, &primary); err != nil {
			break
		}
		present[name] = true
	}
	if closeErr := columns.Close(); err != nil || closeErr != nil {
		t.Errorf("raw audit columns scan=%v close=%v", err, closeErr)
		return 0
	}
	queries := []struct{ name, query string }{
		{"metadata_cache", "SELECT COALESCE(SUM(request_metadata_bytes),0) FROM work_requests WHERE job_id=?"},
		{"request_json", "SELECT COALESCE(SUM(COALESCE(LENGTH(CAST(members AS BLOB)),0)+COALESCE(LENGTH(CAST(debits AS BLOB)),0)+COALESCE(LENGTH(CAST(segment_ids AS BLOB)),0)),0) FROM work_requests WHERE job_id=?"},
		{"request_public", "SELECT COALESCE(SUM(COALESCE(LENGTH(CAST(glossary_receipt AS BLOB)),0)+COALESCE(LENGTH(capture_payload),0)+COALESCE(LENGTH(response_glossary),0)),0) FROM work_requests WHERE job_id=?"},
		{"input", "SELECT COALESCE(SUM(COALESCE(LENGTH(payload),0)+LENGTH(CAST(prompt_id AS BLOB))+LENGTH(CAST(work_id AS BLOB))+LENGTH(CAST(candidate_id AS BLOB))+LENGTH(CAST(baseline_digest AS BLOB))+LENGTH(CAST(baseline_status AS BLOB))+LENGTH(CAST(payload_digest AS BLOB))),0) FROM work_request_input_members WHERE request_id IN (SELECT id FROM work_requests WHERE job_id=?)"},
		{"output", "SELECT COALESCE(SUM(COALESCE(LENGTH(payload),0)+COALESCE(LENGTH(corrected_payload),0)+LENGTH(CAST(payload_digest AS BLOB))+LENGTH(CAST(corrected_digest AS BLOB))+LENGTH(CAST(failure AS BLOB))+LENGTH(CAST(candidate_digest AS BLOB))),0) FROM work_response_members WHERE request_id IN (SELECT id FROM work_requests WHERE job_id=?)"},
		{"candidate", "SELECT COALESCE(SUM(COALESCE(LENGTH(CAST(payload AS BLOB)),0)+COALESCE(LENGTH(pipeline_header),0)+COALESCE(LENGTH(CAST(baseline_target AS BLOB)),0)),0) FROM work_candidates WHERE work_item_id IN (SELECT id FROM work_items WHERE job_id=?)"},
	}
	var legacy []string
	for _, column := range []string{"freeze_payload", "response_payload", "corrected_targets", "corrected_target_index", "promotion_members", "promotion_status"} {
		if present[column] {
			legacy = append(legacy, "COALESCE(LENGTH(CAST("+column+" AS BLOB)),0)")
		}
	}
	if len(legacy) > 0 {
		queries = append(queries, struct{ name, query string }{"request_legacy", "SELECT COALESCE(SUM(" + strings.Join(legacy, "+") + "),0) FROM work_requests WHERE job_id=?"})
	}
	contributions := map[string]int64{}
	var total int64
	for _, q := range queries {
		rows, err := client.QueryContext(ctx, q.query, jobID)
		if err != nil {
			t.Errorf("raw audit %s: %v", q.name, err)
			continue
		}
		var n int64
		if rows.Next() {
			err = rows.Scan(&n)
		}
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			t.Errorf("raw audit %s scan=%v close=%v", q.name, err, closeErr)
		}
		t.Logf("raw_storage_audit %s=%d", q.name, n)
		contributions[q.name] = n
		if q.name != "metadata_cache" {
			total += n
		}
	}
	if contributions["metadata_cache"] != contributions["request_json"] {
		t.Errorf("request metadata cache=%d actual JSON=%d", contributions["metadata_cache"], contributions["request_json"])
	}
	return total
}
