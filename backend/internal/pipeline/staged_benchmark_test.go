package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

const controlledSegments = 24
const controlledBatchSize = 4
const controlledTarget = "<ruby>alpha<rt>reading</rt></ruby> beta"

type controlledCase struct {
	name         string
	main, align  int
	legacy       bool
	serial       bool
	shared       bool
	batch, words int
	wait         time.Duration
}

var controlledCases = []controlledCase{
	{name: "legacy_serial_C1", main: 1, legacy: true, serial: true, shared: true},
	{name: "corrected_serial_C1", main: 1, serial: true, shared: true},
	{name: "shared_pipeline_C1", main: 1, shared: true},
	{name: "independent_pipeline_C1_A1", main: 1, align: 1},
	{name: "legacy_serial_C2", main: 2, legacy: true, serial: true, shared: true},
	{name: "corrected_serial_C2", main: 2, serial: true, shared: true},
	{name: "shared_pipeline_C2", main: 2, shared: true},
	{name: "independent_pipeline_C2_A2", main: 2, align: 2},
	{name: "independent_pipeline_C2_A4", main: 2, align: 4},
	{name: "batch_C1_A1_B4", main: 1, align: 1, batch: 4, wait: 25 * time.Millisecond},
	{name: "batch_C1_A1_B8", main: 1, align: 1, batch: 8, wait: 25 * time.Millisecond},
	{name: "batch_C1_A1_words24", main: 1, align: 1, words: 24, wait: 25 * time.Millisecond},
}

type controlledMetrics struct {
	mainCalls, alignmentCalls, tokens   int
	mainPeak, alignmentPeak, totalPeak  int
	backendPeak                         int
	bytesPeak                           int64
	firstVisible, lastMain              time.Time
	alignmentMembers, maxAlignmentBatch int
}

type controlledHTTP struct {
	mu                              sync.Mutex
	metrics                         controlledMetrics
	mainInflight, alignmentInflight int
	backendInflight                 map[int]int
	candidates                      map[string]int64
	bytes                           int64
	joined                          sync.WaitGroup
	server                          *httptest.Server
	pool                            *backend.LimiterPool
	sameBackend                     bool
}

func newControlledHTTP(tb testing.TB, sameBackend bool, rpm int) *controlledHTTP {
	tb.Helper()
	f := &controlledHTTP{sameBackend: sameBackend, pool: backend.NewLimiterPool()}
	f.reset()
	f.pool.Initialize(map[int]int{1: rpm, 2: rpm})
	tb.Cleanup(f.pool.Shutdown)
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	tb.Cleanup(f.server.Close)
	return f
}

func (f *controlledHTTP) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics = controlledMetrics{}
	f.mainInflight, f.alignmentInflight, f.bytes = 0, 0, 0
	f.backendInflight = map[int]int{}
	f.candidates = map[string]int64{}
}

func (f *controlledHTTP) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.joined.Add(1)
	defer f.joined.Done()
	alignment := r.URL.Path == "/alignment"
	id := 1
	if alignment && !f.sameBackend {
		id = 2
	}
	f.mu.Lock()
	if alignment {
		f.alignmentInflight++
		f.metrics.alignmentCalls++
	} else {
		f.mainInflight++
		f.metrics.mainCalls++
	}
	f.backendInflight[id]++
	f.metrics.tokens += 18
	f.metrics.mainPeak = max(f.metrics.mainPeak, f.mainInflight)
	f.metrics.alignmentPeak = max(f.metrics.alignmentPeak, f.alignmentInflight)
	f.metrics.totalPeak = max(f.metrics.totalPeak, f.mainInflight+f.alignmentInflight)
	f.metrics.backendPeak = max(f.metrics.backendPeak, f.backendInflight[id])
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if alignment {
			f.alignmentInflight--
		} else {
			f.mainInflight--
			f.metrics.lastMain = time.Now()
		}
		f.backendInflight[id]--
	}()
	var request backend.Request
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	delay := 4 * time.Millisecond
	if alignment {
		delay = 12 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.Context().Done():
		return
	}
	text := `{"ruby_output":[{"id":"1","base":"alpha","text":"reading","kind":"creative","occurrence":1}]}`
	if alignment {
		var batch struct {
			Alignments []struct {
				WorkID      string `json:"work_id"`
				CandidateID string `json:"candidate_id"`
			} `json:"alignments"`
		}
		if err := json.Unmarshal([]byte(request.User), &batch); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		n := max(1, len(batch.Alignments))
		f.mu.Lock()
		f.metrics.alignmentMembers += n
		f.metrics.maxAlignmentBatch = max(f.metrics.maxAlignmentBatch, n)
		f.mu.Unlock()
		if len(batch.Alignments) > 0 {
			var rows []map[string]any
			// Return reversed members to exercise identity association at transport.
			for i := len(batch.Alignments) - 1; i >= 0; i-- {
				member := batch.Alignments[i]
				rows = append(rows, map[string]any{"work_id": member.WorkID, "candidate_id": member.CandidateID, "ruby_output": []map[string]any{{"id": "1", "base": "alpha", "text": "reading", "kind": "creative", "occurrence": 1}}})
			}
			encoded, err := json.Marshal(map[string]any{"alignments": rows})
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			text = string(encoded)
		}
	}
	if !alignment {
		var input struct {
			Segments map[string]prompt.SegmentDetail `json:"segments"`
		}
		if err := json.Unmarshal([]byte(request.User), &input); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		translations := make(map[string]string, len(input.Segments))
		for key, segment := range input.Segments {
			if segment.Translate {
				translations[key] = "alpha beta"
			}
		}
		encoded, err := json.Marshal(map[string]any{"translations": translations})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		text = string(encoded)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(backend.Response{Text: text, Usage: backend.Usage{PromptTokens: 7, CompletionTokens: 11}})
}

type controlledBackend struct {
	client   *http.Client
	endpoint string
}

func (b *controlledBackend) Name() string { return b.endpoint }
func (*controlledBackend) Close() error   { return nil }
func (b *controlledBackend) Translate(ctx context.Context, request backend.Request) (*backend.Response, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("controlled backend HTTP %d", resp.StatusCode)
	}
	var result backend.Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

type controlledStore struct {
	*MemoryRoundStore
	fixture *controlledHTTP
}

func (s *controlledStore) Save(ctx context.Context, c *Candidate) error {
	data, err := c.Encode()
	if err != nil {
		return err
	}
	if err = s.MemoryRoundStore.Save(ctx, c); err != nil {
		return err
	}
	f := s.fixture
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes += int64(len(data)) - f.candidates[c.ID]
	f.candidates[c.ID] = int64(len(data))
	f.metrics.bytesPeak = max(f.metrics.bytesPeak, f.bytes)
	return nil
}

func (s *controlledStore) Commit(ctx context.Context, c *Candidate, result TranslatedSegment) (CommitOutcome, error) {
	outcome, err := s.MemoryRoundStore.Commit(ctx, c, result)
	if err != nil {
		return outcome, err
	}
	f := s.fixture
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes -= f.candidates[c.ID]
	delete(f.candidates, c.ID)
	if f.metrics.firstVisible.IsZero() {
		f.metrics.firstVisible = time.Now()
	}
	return outcome, nil
}

func runControlledPipeline(ctx context.Context, f *controlledHTTP, tc controlledCase) (*Document, error) {
	doc := newTestDoc(controlledSegments)
	doc.Format = "text"
	for i := range doc.Segments {
		doc.Segments[i].Meta = map[string]any{"ruby_items": []ruby.Item{{ID: "1", SourceBase: "source", SourceText: "reading"}}}
	}
	renderer, err := prompt.NewRenderer("controlled translation")
	if err != nil {
		return nil, err
	}
	a, err := backend.NewRequestAdmission(f.pool, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: tc.main}, AlignmentConcurrency: tc.align, LegacyRoundShared: tc.shared})
	if err != nil {
		return nil, err
	}
	runtime := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	defer runtime.Close()
	makeBackend := func(stage string, id int) backend.Backend {
		limiter, _ := f.pool.Lookup(id)
		var b backend.Backend = &controlledBackend{client: backend.LimitResponseClient(f.server.Client(), backend.DefaultMaxResponseBytes), endpoint: f.server.URL + "/" + stage}
		b = backend.NewRateLimitedBackend(b, limiter)
		if !tc.legacy {
			b = BindRequestBackend(b, id, 0, runtime)
		}
		return b
	}
	alignID := 2
	if f.sameBackend {
		alignID = 1
	}
	h := &TranslateHandler{Backend: makeBackend("main", 1), BatchSize: controlledBatchSize, Renderer: renderer, RubyEnabled: true, RubyProtocolVersion: ruby.ProtocolV2,
		RubyRetryBackends: []backend.Backend{makeBackend("alignment", alignID)}, RubyRetryAttempts: 1, RubyTemplates: testRubyTemplates(), Logger: quietLogger()}
	if tc.batch > 0 || tc.words > 0 {
		h.RubyBatch = AlignmentBatchConfig{BatchSize: tc.batch, MaxWordsPerBatch: tc.words, Wait: tc.wait, ProtocolVersion: ruby.BatchProtocolVersion}
		h.RubyTemplates.BatchJSON, h.RubyTemplates.BatchText = prompt.RubyAlignmentBatchJSONTemplate, prompt.RubyAlignmentBatchTextTemplate
	}
	round := Round{Concurrency: tc.main, Handler: h, Runtime: runtime}
	store := &controlledStore{MemoryRoundStore: NewMemoryRoundStore(nil), fixture: f}
	if !tc.serial {
		result, err := RunStagedRound(ctx, round, doc, store, runtime, quietLogger(), progress.Nop{})
		if err != nil {
			return nil, err
		}
		if len(result.Unresolved) > 0 {
			return nil, fmt.Errorf("staged unresolved: %v", result.Unresolved)
		}
		return doc, nil
	}
	// Reconstruct batch-worker serialization for the two control groups. The
	// legacy path uses the retained original inline restorer; corrected serial
	// uses production candidate preparation/finalization without stage overlap.
	batches, err := h.BuildBatches(ctx, doc, nil, 0)
	if err != nil {
		return nil, err
	}
	type serialBatch struct {
		indices  []int
		snapshot *Document
	}
	jobs := make(chan serialBatch, len(batches))
	for _, ids := range batches {
		jobs <- serialBatch{ids, snapshotDocument(doc, ids)}
	}
	close(jobs)
	var wg sync.WaitGroup
	errs := make(chan error, tc.main)
	for range tc.main {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range jobs {
				if tc.legacy {
					result := h.ProcessBatch(ctx, batch.snapshot, batch.indices, 0, quietLogger())
					if result.err != nil || len(result.unresolved) > 0 || result.callbackResult == nil {
						errs <- fmt.Errorf("legacy batch failed: %v", result.err)
						return
					}
					for _, idx := range batch.indices {
						doc.Segments[idx] = batch.snapshot.Segments[idx]
					}
					f.mu.Lock()
					if f.metrics.firstVisible.IsZero() {
						f.metrics.firstVisible = time.Now()
					}
					f.mu.Unlock()
					continue
				}
				reservation := NewWorkID()
				for {
					changed := runtime.Window.Changed()
					if runtime.Window.TryReserve(reservation, len(batch.indices)) {
						break
					}
					select {
					case <-changed:
					case <-ctx.Done():
						errs <- ctx.Err()
						return
					}
				}
				event := prepareMain(ctx, round, h, batch.snapshot, batch.indices, 0, 0, reservation, store, runtime, quietLogger())
				if event.err != nil || len(event.candidates) != len(batch.indices) {
					errs <- fmt.Errorf("corrected batch failed: %v", event.err)
					return
				}
				for _, c := range event.candidates {
					result := processCandidate(ctx, round, c, store, runtime, quietLogger(), progress.Nop{})
					if result.err != nil || result.completed == nil {
						errs <- fmt.Errorf("corrected candidate failed: %v", result.err)
						return
					}
					doc.Segments[c.Index].Target = result.completed.TargetText
					doc.Segments[c.Index].Issues = result.completed.Issues
					runtime.Window.Release(c.ID)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return doc, nil
}

func checkControlledRun(tb testing.TB, f *controlledHTTP, doc *Document, tc controlledCase) controlledMetrics {
	tb.Helper()
	f.joined.Wait()
	for i, seg := range doc.Segments {
		if seg.Target != controlledTarget || len(seg.Issues) > 0 {
			tb.Fatalf("%s segment %d quality differs: %q issues=%v", tc.name, i, seg.Target, seg.Issues)
		}
	}
	f.mu.Lock()
	m := f.metrics
	f.mu.Unlock()
	if m.mainCalls != controlledSegments/controlledBatchSize || m.alignmentMembers != controlledSegments || m.tokens != 18*(m.mainCalls+m.alignmentCalls) {
		tb.Fatalf("%s changed work or usage: %+v", tc.name, m)
	}
	if tc.batch == 0 && tc.words == 0 && m.alignmentCalls != controlledSegments {
		tb.Fatalf("single-paragraph baseline changed: %+v", m)
	}
	if tc.batch > 0 && m.maxAlignmentBatch > tc.batch {
		tb.Fatalf("batch exceeded configured segment limit: %+v", m)
	}
	if m.mainPeak > tc.main || (tc.shared && m.totalPeak > tc.main) || (!tc.shared && m.alignmentPeak > tc.align) {
		tb.Fatalf("%s exceeded HTTP budgets: %+v", tc.name, m)
	}
	return m
}

func TestRubyAlignmentControlledHTTPBudgets(t *testing.T) {
	for _, tc := range []controlledCase{controlledCases[2], controlledCases[3], controlledCases[6], controlledCases[8]} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControlledHTTP(t, true, 0)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			doc, err := runControlledPipeline(ctx, f, tc)
			if err != nil {
				t.Fatal(err)
			}
			m := checkControlledRun(t, f, doc, tc)
			if !tc.shared && m.totalPeak <= tc.main {
				t.Fatalf("independent stages failed to overlap: %+v", m)
			}
			if tc.align > 1 && m.alignmentPeak < 2 {
				t.Fatalf("explicit A>1 remained serial: %+v", m)
			}
		})
	}
}

// BenchmarkRubyAlignmentPipeline uses 24 fixed one-item paragraphs, main batch
// size 4, 4 ms main and 12 ms alignment latency, 6000 RPM per backend, a 256/16 MiB
// candidate window, 1 MiB item/response limits, no errors or retries, and identical
// verified output. "legacy" reconstructs the old scheduling/protocol with the
// current single-attempt HTTP transport; it is not a historical binary benchmark.
// P2 cases fix C/A and capacity, varying only content batching. Fake usage is
// fixed per invocation and measures accounting, not real model token savings.
func BenchmarkRubyAlignmentPipeline(b *testing.B) {
	for _, same := range []bool{true, false} {
		name := "separate_backends"
		if same {
			name = "same_backend"
		}
		b.Run(name, func(b *testing.B) {
			for _, tc := range controlledCases {
				b.Run(tc.name, func(b *testing.B) {
					f := newControlledHTTP(b, same, 6000)
					b.ReportAllocs()
					var total, tail, visible time.Duration
					var metrics controlledMetrics
					b.ResetTimer()
					for range b.N {
						f.reset()
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						start := time.Now()
						doc, err := runControlledPipeline(ctx, f, tc)
						end := time.Now()
						cancel()
						if err != nil {
							b.Fatal(err)
						}
						m := checkControlledRun(b, f, doc, tc)
						total += end.Sub(start)
						tail += end.Sub(m.lastMain)
						visible += m.firstVisible.Sub(start)
						metrics = m
					}
					b.ReportMetric(float64(total.Microseconds())/float64(b.N)/1000, "total_ms/op")
					b.ReportMetric(float64(tail.Microseconds())/float64(b.N)/1000, "tail_ms/op")
					b.ReportMetric(float64(visible.Microseconds())/float64(b.N)/1000, "first_ms/op")
					b.ReportMetric(float64(metrics.mainCalls+metrics.alignmentCalls), "HTTP/op")
					b.ReportMetric(float64(metrics.tokens), "tokens/op")
					b.ReportMetric(float64(metrics.alignmentCalls), "alignment_HTTP/op")
					b.ReportMetric(float64(metrics.maxAlignmentBatch), "max_alignment_batch")
					b.ReportMetric(float64(metrics.mainPeak), "main_peak")
					b.ReportMetric(float64(metrics.alignmentPeak), "alignment_peak")
					b.ReportMetric(float64(metrics.totalPeak), "total_peak")
					b.ReportMetric(float64(metrics.backendPeak), "backend_peak")
					b.ReportMetric(float64(metrics.bytesPeak), "candidate_bytes_peak")
				})
			}
		})
	}
}
