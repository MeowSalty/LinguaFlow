package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
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
		actor := m.rootActor
		if actor == 0 {
			actor = 1
		}
		m.peakRoot = max(m.peakRoot, m.root.Snapshot(actor).Instance.PayloadBytes)
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

func TestMemberResponseWorkMemory(t *testing.T) {
	for _, n := range []int{32, 64, 128, 256} {
		t.Run(fmt.Sprintf("N%d", n), func(t *testing.T) {
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: fmt.Sprintf("member-work-%d", n)}); err != nil {
				t.Fatal(err)
			}
			store := NewMemoryRoundStore(nil)
			defer store.Close()
			doc := newTestDoc(n)
			defer r.CloseDocument(doc)
			source := strings.TrimSpace(strings.Repeat(strings.Repeat("s", 72)+" ", 14))
			target := strings.Repeat("translation", 96)
			translations := make(map[string]string, n)
			for i := range doc.Segments {
				doc.Segments[i].Source = source
				translations[fmt.Sprint(i+1)] = target
			}
			raw, err := json.Marshal(map[string]any{"translations": translations})
			if err != nil {
				t.Fatal(err)
			}
			main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: string(raw), Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
			}}
			handler := stagedTestHandler(t, r, main)
			handler.BatchSize, handler.MaxWordsPerBatch = 0, 4000
			totals := &memberWorkTotals{root: root}
			ctx := backend.WithMemberWorkRecorder(t.Context(), totals.observe)
			result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: handler}, doc, store, r, quietLogger(), progress.Nop{})
			if err != nil || len(result.Resolved) != n || main.calls.Load() != 1 {
				t.Fatalf("full chain: resolved=%d/%d calls=%d err=%v", len(result.Resolved), n, main.calls.Load(), err)
			}
			for i, seg := range doc.Segments {
				if seg.Target != target {
					t.Fatalf("member %d target mismatch", i)
				}
			}
			if doc.InputTokens != 17 || doc.OutputTokens != 3 {
				t.Fatalf("usage changed: %d/%d", doc.InputTokens, doc.OutputTokens)
			}
			totals.check(t, n, int64(n*(len(source)+len(target))+len(raw)), int64(len(raw)))
			owned := r.Admission.OwnedPayloadSnapshot()
			if owned.TotalPeak <= 0 || owned.TotalPeak > memberWorkBytePasses*int64(n*(len(source)+len(target))+len(raw))+memberWorkMetadataBytes*int64(n) {
				t.Fatalf("actual observed backing peak exceeds fixed bound: %+v", owned)
			}
			for _, category := range []string{"freeze", "raw", "wire", "candidate"} {
				if owned.Current[category] != 0 {
					t.Fatalf("terminal backing %s remains: %+v", category, owned)
				}
			}
			t.Logf("actual_backing_peak=%d terminal_categories=%v", owned.TotalPeak, owned.Current)
			if got := r.Window.Snapshot(); got.Segments != 0 || got.Bytes != 0 {
				t.Fatalf("live inventory: %+v", got)
			}
			r.CloseDocument(doc)
			r.Close()
			retained := root.Snapshot(1).Instance
			if retained.PayloadBytes <= 0 || retained.UnconfirmedMembers != 0 {
				t.Fatalf("durable audit baseline: %+v", retained)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			if got := root.Snapshot(1).Instance; got != (resourcebudget.Vector{}) {
				t.Fatalf("last store reference leaked Root: %+v", got)
			}
			t.Logf("durable_after_runtime_close=%+v after_store_close=zero calls=1", retained)
		})
	}
}
