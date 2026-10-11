package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

const pauseMemorySourceBytes = 4 << 10
const pauseMemoryTranslationBytes = 64 << 10

type pauseMemoryCase struct {
	main, alignment int
	sameBackend     bool
	mainBatch       int
	batch, words    int
}

type pauseMemoryMetrics struct {
	mainCalls, alignmentCalls       int
	alignmentMembers                int
	savedAlignmentMembers           int
	mainInflight, alignmentInflight int
	mainPeak, alignmentPeak         int
	savedBytes, savedPeak           int64
	windowBytesPeak                 int64
	reservedBytesPeak               int64
	windowSegmentsPeak              int
	saveBlocked                     int
	drain                           time.Duration
	heapBaseline, heapPeak          uint64
}

type pauseMemoryHeap struct {
	mu             sync.Mutex
	baseline, peak uint64
}

func (h *pauseMemoryHeap) sample() {
	h.mu.Lock()
	defer h.mu.Unlock()
	var stats goruntime.MemStats
	goruntime.ReadMemStats(&stats)
	h.peak = max(h.peak, stats.HeapAlloc)
}

// pauseMemoryFixture fixes the pause boundary with channels, not wall-clock
// delays: the warmup batch is saved, A alignment HTTP handlers block, then C
// further main responses can reach the local save barrier. No model errors,
// transient storage errors or simulated service latency are introduced.
type pauseMemoryFixture struct {
	mu          sync.Mutex
	tc          pauseMemoryCase
	metrics     pauseMemoryMetrics
	runtime     *ExecutionRuntime
	heap        *pauseMemoryHeap
	saved       map[string]int64
	translation string
	mainBatch   int

	otherMainStarted int
	responsesReady   chan struct{}
	responsesOnce    sync.Once
	networkRelease   chan struct{}
	networkOnce      sync.Once
	saveEntered      chan struct{}
	saveOnce         sync.Once
	saveRelease      chan struct{}
	saveReleaseOnce  sync.Once
	joined           sync.WaitGroup
}

func (f *pauseMemoryFixture) release() {
	f.saveReleaseOnce.Do(func() { close(f.saveRelease) })
	f.networkOnce.Do(func() { close(f.networkRelease) })
}

func (f *pauseMemoryFixture) observe() {
	w := f.runtime.Window
	w.mu.Lock()
	windowBytes, windowSegments, reservedBytes := w.bytes, w.count, int64(0)
	for _, entry := range w.entries {
		if entry.reservation {
			reservedBytes += entry.bytes
		}
	}
	w.mu.Unlock()
	f.mu.Lock()
	f.metrics.windowBytesPeak = max(f.metrics.windowBytesPeak, windowBytes)
	f.metrics.windowSegmentsPeak = max(f.metrics.windowSegmentsPeak, windowSegments)
	f.metrics.reservedBytesPeak = max(f.metrics.reservedBytesPeak, reservedBytes)
	f.mu.Unlock()
	if f.heap != nil {
		f.heap.sample()
	}
}

func (f *pauseMemoryFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.joined.Add(1)
	defer f.joined.Done()
	var request backend.Request
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	alignment := r.URL.Path == "/alignment"
	warmup := false
	var segments map[string]prompt.SegmentDetail
	var alignments []struct {
		WorkID      string `json:"work_id"`
		CandidateID string `json:"candidate_id"`
	}
	if alignment {
		input := struct {
			Alignments json.RawMessage `json:"alignments"`
		}{}
		if err := json.Unmarshal([]byte(request.User), &input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(input.Alignments) > 0 {
			if err := json.Unmarshal(input.Alignments, &alignments); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	} else {
		var input struct {
			Segments map[string]prompt.SegmentDetail `json:"segments"`
		}
		if err := json.Unmarshal([]byte(request.User), &input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		segments = input.Segments
		for _, segment := range segments {
			warmup = warmup || strings.HasPrefix(segment.Source, "warmup:")
		}
	}
	f.mu.Lock()
	if alignment {
		f.metrics.alignmentCalls++
		f.metrics.alignmentMembers += max(1, len(alignments))
		f.metrics.alignmentInflight++
		f.metrics.alignmentPeak = max(f.metrics.alignmentPeak, f.metrics.alignmentInflight)
	} else {
		f.metrics.mainCalls++
		f.metrics.mainInflight++
		f.metrics.mainPeak = max(f.metrics.mainPeak, f.metrics.mainInflight)
		if !warmup {
			f.otherMainStarted++
		}
	}
	if f.metrics.alignmentInflight == f.tc.alignment && f.otherMainStarted == f.tc.main {
		f.responsesOnce.Do(func() { close(f.responsesReady) })
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if alignment {
			f.metrics.alignmentInflight--
		} else {
			f.metrics.mainInflight--
		}
	}()
	f.observe()
	if alignment {
		select {
		case <-f.networkRelease:
		case <-r.Context().Done():
			return
		}
	} else if !warmup {
		select {
		case <-f.responsesReady:
		case <-r.Context().Done():
			return
		}
	}
	text := `{"ruby_output":[{"id":"1","base":"alpha","text":"reading","kind":"creative","occurrence":1}]}`
	if len(alignments) > 0 {
		members := make([]map[string]any, 0, len(alignments))
		for i := len(alignments) - 1; i >= 0; i-- {
			member := alignments[i]
			members = append(members, map[string]any{
				"work_id": member.WorkID, "candidate_id": member.CandidateID,
				"ruby_output": []ruby.OutputEntry{{ID: "1", Base: "alpha", Text: "reading", Kind: "creative", Occurrence: 1}},
			})
		}
		data, err := json.Marshal(map[string]any{"alignments": members})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		text = string(data)
	} else if !alignment {
		translations := make(map[string]string, len(segments))
		for id, segment := range segments {
			if segment.Translate {
				translations[id] = f.translation
			}
		}
		data, err := json.Marshal(map[string]any{"translations": translations})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		text = string(data)
	}
	payload, err := json.Marshal(backend.Response{Text: text, Usage: backend.Usage{PromptTokens: 7, CompletionTokens: 11}})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f.observe()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

type pauseMemoryStore struct {
	*MemoryRoundStore
	fixture *pauseMemoryFixture
}

func (s *pauseMemoryStore) Reserve(ctx context.Context, intent RequestIntent) error {
	s.fixture.observe()
	return s.MemoryRoundStore.Reserve(ctx, intent)
}

func (s *pauseMemoryStore) Record(ctx context.Context, id string, record RequestRecord) error {
	s.fixture.observe()
	return s.MemoryRoundStore.Record(ctx, id, record)
}

func (s *pauseMemoryStore) Save(ctx context.Context, c *Candidate) error {
	f := s.fixture
	f.observe()
	if c.Index >= f.mainBatch && c.Version == 1 {
		f.mu.Lock()
		f.metrics.saveBlocked++
		f.mu.Unlock()
		f.saveOnce.Do(func() { close(f.saveEntered) })
		select {
		case <-f.saveRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
		f.mu.Lock()
		f.metrics.saveBlocked--
		f.mu.Unlock()
	}
	if err := s.MemoryRoundStore.Save(ctx, c); err != nil {
		return err
	}
	f.mu.Lock()
	f.metrics.savedBytes += c.StoredBytes - f.saved[c.ID]
	f.saved[c.ID] = c.StoredBytes
	f.metrics.savedPeak = max(f.metrics.savedPeak, f.metrics.savedBytes)
	if c.Version == 2 && c.Ready && c.LogicalAttempt == 1 {
		f.metrics.savedAlignmentMembers++
	}
	f.mu.Unlock()
	f.observe()
	return nil
}

func (s *pauseMemoryStore) Commit(ctx context.Context, c *Candidate, result TranslatedSegment) (CommitOutcome, error) {
	outcome, err := s.MemoryRoundStore.Commit(ctx, c, result)
	if err != nil {
		return outcome, err
	}
	f := s.fixture
	f.mu.Lock()
	f.metrics.savedBytes -= f.saved[c.ID]
	delete(f.saved, c.ID)
	f.mu.Unlock()
	f.observe()
	return outcome, nil
}

func runPauseMemoryBenchmark(tc pauseMemoryCase, sampleHeap bool) (metrics pauseMemoryMetrics, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 6000, 2: 6000})
	defer pool.Shutdown()
	admission, err := backend.NewRequestAdmission(pool, backend.RequestAdmissionConfig{
		MainConcurrency: map[int]int{0: tc.main}, AlignmentConcurrency: tc.alignment,
	})
	if err != nil {
		return metrics, err
	}
	runtime := NewExecutionRuntime(admission, DefaultCandidateLimits(), nil)
	defer runtime.Close()
	mainBatch := tc.mainBatch
	if mainBatch == 0 {
		mainBatch = controlledBatchSize
	}
	f := &pauseMemoryFixture{
		tc: tc, runtime: runtime, saved: map[string]int64{}, mainBatch: mainBatch,
		responsesReady: make(chan struct{}), networkRelease: make(chan struct{}),
		saveEntered: make(chan struct{}), saveRelease: make(chan struct{}),
		translation: "alpha beta" + strings.Repeat("x", pauseMemoryTranslationBytes-len("alpha beta")),
	}
	if sampleHeap {
		f.heap = &pauseMemoryHeap{}
	}
	defer f.release()
	server := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := backend.LimitResponseClient(&http.Client{Transport: transport}, backend.DefaultMaxResponseBytes)
	makeBackend := func(stage string, id int) backend.Backend {
		limiter, _ := pool.Lookup(id)
		var b backend.Backend = &controlledBackend{client: client, endpoint: server.URL + "/" + stage}
		b = backend.NewRateLimitedBackend(b, limiter)
		return BindRequestBackend(b, id, 0, runtime)
	}
	alignID := 2
	if tc.sameBackend {
		alignID = 1
	}
	renderer, err := prompt.NewRenderer("controlled translation")
	if err != nil {
		return metrics, err
	}
	handler := &TranslateHandler{
		Backend: makeBackend("main", 1), BatchSize: mainBatch,
		Renderer: renderer, RubyEnabled: true, RubyProtocolVersion: ruby.ProtocolV2,
		RubyRetryBackends: []backend.Backend{makeBackend("alignment", alignID)},
		RubyRetryAttempts: 1, RubyTemplates: testRubyTemplates(), Logger: quietLogger(),
	}
	if tc.batch > 0 || tc.words > 0 {
		handler.RubyBatch = AlignmentBatchConfig{BatchSize: tc.batch, MaxWordsPerBatch: tc.words, Wait: 25 * time.Millisecond, ProtocolVersion: ruby.BatchProtocolVersion}
		handler.RubyTemplates.BatchJSON = prompt.RubyAlignmentBatchJSONTemplate
		handler.RubyTemplates.BatchText = prompt.RubyAlignmentBatchTextTemplate
	}
	round := Round{Concurrency: tc.main, Handler: handler, Runtime: runtime}
	store := &pauseMemoryStore{MemoryRoundStore: NewMemoryRoundStore(nil), fixture: f}
	doc := newTestDoc(controlledSegments)
	doc.Format = "text"
	for index := range doc.Segments {
		prefix := fmt.Sprintf("segment:%d ", index)
		if index < mainBatch {
			prefix = "warmup:" + prefix
		}
		source := prefix + strings.Repeat("s", pauseMemorySourceBytes-len(prefix))
		doc.Segments[index].Source = source
		doc.Segments[index].OriginalSource = source
		doc.Segments[index].Meta = map[string]any{"ruby_items": []ruby.Item{{ID: "1", SourceBase: "source", SourceText: "reading"}}}
	}
	stopSampling, samplingDone := make(chan struct{}), make(chan struct{})
	if sampleHeap {
		goruntime.GC()
		var baseline goruntime.MemStats
		goruntime.ReadMemStats(&baseline)
		f.heap.mu.Lock()
		f.heap.baseline, f.heap.peak = baseline.HeapAlloc, baseline.HeapAlloc
		f.heap.mu.Unlock()
		go func() {
			defer close(samplingDone)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					f.observe()
				case <-stopSampling:
					return
				}
			}
		}()
		defer func() { close(stopSampling); <-samplingDone }()
	}
	type roundExit struct {
		result RunRoundResult
		err    error
	}
	done := make(chan roundExit, 1)
	joined := false
	go func() {
		result, err := RunStagedRound(ctx, round, doc, store, runtime, quietLogger(), progress.Nop{})
		done <- roundExit{result, err}
	}()
	defer func() {
		cancel()
		f.release()
		if !joined {
			<-done
		}
	}()
	select {
	case <-f.saveEntered:
	case result := <-done:
		joined = true
		return metrics, fmt.Errorf("round exited before pause barrier: %v", result.err)
	case <-ctx.Done():
		return metrics, fmt.Errorf("pause barrier: %w", ctx.Err())
	}
	f.mu.Lock()
	atPause := f.metrics
	f.mu.Unlock()
	if atPause.mainCalls != tc.main+1 || atPause.alignmentInflight != tc.alignment || atPause.saveBlocked != 1 {
		return metrics, fmt.Errorf("pause did not hold the requested HTTP/save work: %+v", atPause)
	}
	f.observe()
	start := time.Now()
	runtime.Gate.Pause()
	f.release()
	var exit roundExit
	select {
	case exit = <-done:
		joined = true
	case <-ctx.Done():
		return metrics, fmt.Errorf("pause drain: %w", ctx.Err())
	}
	drain := time.Since(start)
	if exit.err != nil {
		return metrics, exit.err
	}
	f.joined.Wait()
	f.observe()
	f.mu.Lock()
	metrics = f.metrics
	f.mu.Unlock()
	metrics.drain = drain
	if sampleHeap {
		f.heap.mu.Lock()
		metrics.heapBaseline, metrics.heapPeak = f.heap.baseline, f.heap.peak
		f.heap.mu.Unlock()
	}
	if metrics.mainCalls != atPause.mainCalls || metrics.alignmentCalls != atPause.alignmentCalls || metrics.mainPeak > tc.main || metrics.alignmentPeak > tc.alignment {
		return metrics, fmt.Errorf("pause dispatched more work or exceeded request limits: %+v", metrics)
	}
	limits := DefaultCandidateLimits()
	if metrics.reservedBytesPeak != int64(tc.main*mainBatch)*limits.ItemBytes || metrics.windowSegmentsPeak != (tc.main+1)*mainBatch || metrics.windowBytesPeak > limits.Bytes {
		return metrics, fmt.Errorf("controlled window occupancy differs from reserved work: %+v", metrics)
	}
	state, err := store.Load(ctx)
	if err != nil {
		return metrics, err
	}
	candidates, _, err := store.Candidates(ctx, 0, controlledSegments)
	if err != nil {
		return metrics, err
	}
	wantCandidates := (tc.main+1)*mainBatch - metrics.alignmentMembers
	if len(state.Completed) != metrics.alignmentMembers || metrics.savedAlignmentMembers != metrics.alignmentMembers || len(candidates) != wantCandidates || len(exit.result.Resolved) != metrics.alignmentMembers {
		return metrics, fmt.Errorf("incomplete drain: confirmed=%d drafts=%d result=%+v", len(state.Completed), len(candidates), exit.result)
	}
	window := runtime.Window.Snapshot()
	if window.Segments != wantCandidates || window.Bytes != metrics.savedBytes {
		return metrics, fmt.Errorf("window differs from saved drafts: %+v saved=%d", window, metrics.savedBytes)
	}
	remaining := admission.Snapshot()
	if remaining.MainInflight[0] != 0 || remaining.AlignmentInflight != 0 || remaining.PendingResults != 0 || remaining.QueuedRequests != 0 {
		return metrics, fmt.Errorf("pause returned before requests/completions joined: %+v", remaining)
	}
	expected := strings.Replace(f.translation, "alpha", "<ruby>alpha<rt>reading</rt></ruby>", 1)
	for _, index := range state.Completed {
		if doc.Segments[index].Target != expected || len(doc.Segments[index].Issues) != 0 {
			return metrics, fmt.Errorf("pause changed accepted segment %d quality", index)
		}
	}
	return metrics, nil
}

// BenchmarkRubyAlignmentBatchPause keeps C=1/A=1, main batches of 8, the same
// 24 paragraphs and byte/capacity limits as PauseMemory, and 6000 RPM per backend
// with a full initial burst. Each fixed paragraph contributes 8 content words;
// words24 therefore admits exactly 3 members. Only the alignment batch limit
// varies. Both backend layouts remain separate comparison groups.
//
// The channels establish a common pause point: 8 warmup drafts are saved, one
// alignment HTTP request is blocked, and the second main response is blocked at
// its first candidate save. Gate.Pause then releases both barriers. No latency,
// failures, heap sampling, or explicit GC is injected; pause_us/op includes only
// the remaining response parsing, saves and confirmations until the round joins.
// Setup and reaching the barrier are excluded. In every case exactly 3 HTTP
// requests have started; confirmed/saved alignment members must equal the actual
// membership of the one dispatched alignment request (1/4/8/3 respectively).
func BenchmarkRubyAlignmentBatchPause(b *testing.B) {
	for _, same := range []bool{true, false} {
		backendName := "separate_backends"
		if same {
			backendName = "same_backend"
		}
		b.Run(backendName, func(b *testing.B) {
			for _, config := range []struct {
				name         string
				batch, words int
				wantMembers  int
			}{{"B1", 1, 0, 1}, {"B4", 4, 0, 4}, {"B8", 8, 0, 8}, {"words24", 0, 24, 3}} {
				b.Run("C1_A1_"+config.name, func(b *testing.B) {
					var drain time.Duration
					var last pauseMemoryMetrics
					for range b.N {
						metrics, err := runPauseMemoryBenchmark(pauseMemoryCase{
							main: 1, alignment: 1, sameBackend: same, mainBatch: 8, batch: config.batch, words: config.words,
						}, false)
						if err != nil {
							b.Fatal(err)
						}
						if metrics.mainCalls != 2 || metrics.alignmentCalls != 1 || metrics.alignmentMembers != config.wantMembers || metrics.savedAlignmentMembers != config.wantMembers {
							b.Fatalf("pause drained other than the dispatched membership: %+v", metrics)
						}
						drain += metrics.drain
						last = metrics
					}
					b.ReportMetric(0, "ns/op")
					b.ReportMetric(float64(drain.Nanoseconds())/float64(b.N)/1000, "pause_us/op")
					b.ReportMetric(float64(last.mainCalls+last.alignmentCalls), "HTTP/op")
					b.ReportMetric(float64(last.mainCalls), "main_HTTP/op")
					b.ReportMetric(float64(last.alignmentCalls), "alignment_HTTP/op")
					b.ReportMetric(float64(last.alignmentMembers), "alignment_members/op")
					b.ReportMetric(float64(last.savedAlignmentMembers), "saved_alignment_members/op")
					b.ReportMetric(float64(last.mainPeak), "main_peak")
					b.ReportMetric(float64(last.alignmentPeak), "alignment_peak")
				})
			}
		})
	}
}

// BenchmarkRubyAlignmentPauseMemory uses 24 paragraphs (4 KiB source/64 KiB
// translation each), batches of 4, one Ruby item/paragraph, a 256 segment/16 MiB
// window and 1 MiB candidate/response limits. Each backend has 6000 RPM with a
// full initial burst. The benchmark stops at pause rather than completing all
// 24 paragraphs; admitted requests are exactly 1+C+A, accepted segments are A.
//
// Heap mode forces GC after fixture setup, then observes process HeapAlloc at
// HTTP/store boundaries and with a 1 ms ticker. It includes the in-process HTTP
// server and MemoryRoundStore, is a sampled heap peak rather than RSS, and is not
// a deployment memory bound. Heap sampling is disabled in drain mode so its
// reported pause_us/op excludes ReadMemStats and explicit GC. That latency runs
// from Gate.Pause through releasing both barriers to RunStagedRound returning;
// it includes remaining response parsing/saving/confirmation, without artificial
// server/storage delay. Window metrics are observed from the real reservation
// ledger; saved bytes measure encoded payloads retained by the store.
func BenchmarkRubyAlignmentPauseMemory(b *testing.B) {
	for _, sampleHeap := range []bool{true, false} {
		mode := "drain"
		if sampleHeap {
			mode = "heap"
		}
		b.Run(mode, func(b *testing.B) {
			for _, same := range []bool{true, false} {
				backendName := "separate_backends"
				if same {
					backendName = "same_backend"
				}
				b.Run(backendName, func(b *testing.B) {
					for _, capacity := range [][2]int{{1, 1}, {2, 2}, {2, 4}} {
						tc := pauseMemoryCase{main: capacity[0], alignment: capacity[1], sameBackend: same}
						b.Run(fmt.Sprintf("C%d_A%d", tc.main, tc.alignment), func(b *testing.B) {
							var totalDrain time.Duration
							var peak pauseMemoryMetrics
							var baselineTotal, deltaTotal uint64
							for range b.N {
								metrics, err := runPauseMemoryBenchmark(tc, sampleHeap)
								if err != nil {
									b.Fatal(err)
								}
								totalDrain += metrics.drain
								peak.mainCalls, peak.alignmentCalls = metrics.mainCalls, metrics.alignmentCalls
								peak.mainPeak = max(peak.mainPeak, metrics.mainPeak)
								peak.alignmentPeak = max(peak.alignmentPeak, metrics.alignmentPeak)
								peak.savedPeak = max(peak.savedPeak, metrics.savedPeak)
								peak.windowBytesPeak = max(peak.windowBytesPeak, metrics.windowBytesPeak)
								peak.reservedBytesPeak = max(peak.reservedBytesPeak, metrics.reservedBytesPeak)
								peak.windowSegmentsPeak = max(peak.windowSegmentsPeak, metrics.windowSegmentsPeak)
								peak.heapPeak = max(peak.heapPeak, metrics.heapPeak)
								baselineTotal += metrics.heapBaseline
								deltaTotal += metrics.heapPeak - metrics.heapBaseline
							}
							b.ReportMetric(0, "ns/op")
							b.ReportMetric(float64(peak.mainCalls+peak.alignmentCalls), "HTTP/op")
							b.ReportMetric(float64(peak.savedPeak), "saved_peak_B")
							b.ReportMetric(float64(peak.windowBytesPeak), "window_peak_B")
							b.ReportMetric(float64(peak.reservedBytesPeak), "reserved_peak_B")
							b.ReportMetric(float64(peak.windowSegmentsPeak), "window_segments_peak")
							b.ReportMetric(float64(peak.mainPeak), "main_peak")
							b.ReportMetric(float64(peak.alignmentPeak), "alignment_peak")
							if sampleHeap {
								b.ReportMetric(float64(baselineTotal)/float64(b.N), "heap_baseline_B/op")
								b.ReportMetric(float64(deltaTotal)/float64(b.N), "heap_delta_B/op")
								b.ReportMetric(float64(peak.heapPeak), "heap_observed_peak_B")
							} else {
								b.ReportMetric(float64(totalDrain.Nanoseconds())/float64(b.N)/1000, "pause_us/op")
							}
						})
					}
				})
			}
		})
	}
}
