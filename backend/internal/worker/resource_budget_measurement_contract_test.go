// B-T01: actual persisted freeze + actual complete HTTP body + resident prompts;
// paired content/context changes and a separate only-C comparison.
// B-T10: same tiny candidate and actual aux payload, only W/I changed.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

type actualMainFootprint struct {
	Indices                          []int
	Targets                          []string
	Words, ContextMembers            int
	SourceUTF8, ContextUTF8          int64
	Freeze, Wire, System, User, B, K int64
}

type actualMainObservation struct {
	body           []byte
	system         string
	user           string
	targets        []string
	words          int
	sourceUTF8     int64
	contextUTF8    int64
	contextMembers int
	release        chan struct{}
}

// The server holds the response until the test reads that request's still-live
// SQLite freeze. It measures the actual body bytes, never ContentLength alone.
type actualMainHTTP struct {
	observed            chan actualMainObservation
	stop                chan struct{}
	once                sync.Once
	mu                  sync.Mutex
	calls, active, peak int
}

func (h *actualMainHTTP) open() { h.once.Do(func() { close(h.stop) }) }

func (h *actualMainHTTP) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil || r.ContentLength != int64(len(body)) {
		http.Error(w, "actual complete request bytes disagree with ContentLength", http.StatusBadRequest)
		return
	}
	var wire struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	observation := actualMainObservation{body: body, release: make(chan struct{})}
	for _, message := range wire.Messages {
		switch message.Role {
		case "system":
			observation.system = message.Content
		case "user":
			observation.user = message.Content
		}
	}
	var input struct {
		Segments map[string]prompt.SegmentDetail `json:"segments"`
	}
	if err := json.Unmarshal([]byte(observation.user), &input); err != nil || len(input.Segments) == 0 {
		http.Error(w, "missing real translation payload", http.StatusBadRequest)
		return
	}
	translations := map[string]string{}
	for id, member := range input.Segments {
		if member.Translate {
			observation.targets = append(observation.targets, member.Source)
			observation.words += pipeline.CountWords(member.Source)
			observation.sourceUTF8 += int64(len(member.Source))
			translations[id] = "accepted-" + member.Source
		} else {
			observation.contextMembers++
			observation.contextUTF8 += int64(len(member.Source))
		}
	}
	sort.Strings(observation.targets)
	h.mu.Lock()
	h.calls++
	h.active++
	h.peak = max(h.peak, h.active)
	h.mu.Unlock()
	finished := false
	defer func() {
		if !finished {
			h.mu.Lock()
			h.active--
			h.mu.Unlock()
		}
	}()
	select {
	case h.observed <- observation:
	case <-r.Context().Done():
		return
	}
	select {
	case <-observation.release:
	case <-h.stop:
	case <-r.Context().Done():
		return
	}
	content, err := json.Marshal(map[string]any{"translations": translations})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.mu.Lock()
	h.active--
	h.mu.Unlock()
	finished = true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
	})
}

func measureHeldMain(t *testing.T, f *rubyPipelineFixture, observed actualMainObservation) actualMainFootprint {
	t.Helper()
	rows, err := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if len(row.FreezePayload) == 0 {
			continue
		}
		var freeze struct {
			Members []struct {
				Index   int
				Segment struct{ Source string }
			}
		}
		if err := json.Unmarshal(row.FreezePayload, &freeze); err != nil {
			t.Fatal(err)
		}
		var targets []string
		var indices []int
		for _, member := range freeze.Members {
			targets = append(targets, member.Segment.Source)
			indices = append(indices, member.Index)
		}
		sort.Strings(targets)
		if !reflect.DeepEqual(targets, observed.targets) {
			continue
		}
		sort.Ints(indices)
		if row.FreezeBytes != int64(len(row.FreezePayload)) {
			t.Fatalf("persisted freeze declared %d differs from actual %d", row.FreezeBytes, len(row.FreezePayload))
		}
		result := actualMainFootprint{Indices: indices, Targets: targets, Words: observed.words,
			ContextMembers: observed.contextMembers, SourceUTF8: observed.sourceUTF8,
			ContextUTF8: observed.contextUTF8, Freeze: int64(len(row.FreezePayload)),
			Wire: int64(len(observed.body)), System: int64(len(observed.system)), User: int64(len(observed.user))}
		result.B = result.Wire + result.System + result.User
		result.K = result.Freeze + result.B
		return result
	}
	t.Fatalf("real HTTP target sources have no live matching persisted freeze: %q", observed.targets)
	return actualMainFootprint{}
}

type actualMainCase struct {
	sources            []string
	concurrency, batch int
	context            bool
}

func runActualMainCase(t *testing.T, name string, input actualMainCase) []actualMainFootprint {
	t.Helper()
	h := &actualMainHTTP{observed: make(chan actualMainObservation, len(input.sources)), stop: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(server.Close)
	t.Cleanup(h.open)
	f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), server.URL, len(input.sources))
	f.snapshot.Strategy.Ruby.Enabled = false
	f.snapshot.RubyRetry = nil
	f.snapshot.Strategy.Protect.Enabled = false
	f.snapshot.Strategy.QA.Enabled = false
	f.snapshot.Strategy.Context.Enabled = input.context
	f.snapshot.Strategy.Context.Before, f.snapshot.Strategy.Context.After = 1, 1
	f.snapshot.Rounds[0].Translate.Concurrency = input.concurrency
	f.snapshot.Rounds[0].Translate.BatchSize = input.batch
	f.snapshot.Rounds[0].Translate.MaxWordsPerBatch = 0
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rows := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).Order(ent.Asc(segment.FieldSegmentIndex)).AllX(ctx)
	for i, row := range rows {
		f.client.Segment.UpdateOneID(row.ID).SetSourceText(input.sources[i]).ExecX(ctx)
	}
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, store := f.round(t, 0)
	eng.PrepareDocument(doc, nil)
	type outcome struct {
		result pipeline.RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
		done <- outcome{result, err}
	}()
	joined := false
	defer func() {
		h.open()
		cancel()
		if !joined {
			<-done
		}
	}()
	wantCalls := (len(input.sources) + input.batch - 1) / input.batch
	firstWave := min(input.concurrency, wantCalls)
	var result []actualMainFootprint
	var held []chan struct{}
	for i := 0; i < wantCalls; i++ {
		select {
		case observation := <-h.observed:
			measurement := measureHeldMain(t, f, observation)
			if len(measurement.Indices) != min(input.batch, len(input.sources)-i*input.batch) {
				t.Fatalf("%s changed complete main membership: %+v", name, measurement)
			}
			result = append(result, measurement)
			if i < firstWave {
				held = append(held, observation.release)
			} else {
				close(observation.release)
			}
			if i+1 == firstWave {
				for _, release := range held {
					close(release)
				}
			}
		case out := <-done:
			joined = true
			t.Fatalf("%s ended before actual HTTP %d/%d: %+v %v", name, i, wantCalls, out.result, out.err)
		case <-ctx.Done():
			t.Fatalf("%s actual HTTP barrier timed out: %v", name, ctx.Err())
		}
	}
	var out outcome
	select {
	case out = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatalf("%s did not join: %v", name, ctx.Err())
	}
	if out.err != nil || len(out.result.Resolved) != len(input.sources) || len(out.result.Unresolved) != 0 {
		t.Fatalf("%s actual round failed: %+v %v", name, out.result, out.err)
	}
	h.mu.Lock()
	calls, peak := h.calls, h.peak
	h.mu.Unlock()
	if calls != wantCalls || peak != firstWave {
		t.Fatalf("%s actual main calls/peak=%d/%d want %d/%d", name, calls, peak, wantCalls, firstWave)
	}
	if got := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(context.Background()); got != len(input.sources) {
		t.Fatalf("%s checkpoint count=%d", name, got)
	}
	f.assertUsage(t, wantCalls)
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(context.Background()) {
		if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText {
			t.Fatalf("%s changed accepted target: %+v", name, row)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Indices[0] < result[j].Indices[0] })
	for _, measurement := range result {
		t.Logf("case=%s C=%d batch=%d actual_indices=%v words=%d source_utf8=%d context_members=%d context_utf8=%d freeze=%d actual_wire=%d system=%d user=%d b=%d K=%d main_calls=%d aux_calls=0 peak=%d", name, input.concurrency, input.batch, measurement.Indices, measurement.Words, measurement.SourceUTF8, measurement.ContextMembers, measurement.ContextUTF8, measurement.Freeze, measurement.Wire, measurement.System, measurement.User, measurement.B, measurement.K, calls, peak)
	}
	return result
}

func numberedMeasurementSources(prefix string, n int) []string {
	result := make([]string, n)
	for i := range result {
		result[i] = fmt.Sprintf("%s %03d", prefix, i)
	}
	return result
}

func TestResourceBudgetActualMainFootprintPairs(t *testing.T) {
	t.Run("same_n_short_long", func(t *testing.T) {
		short := runActualMainCase(t, "short", actualMainCase{numberedMeasurementSources("red", 2), 1, 2, false})[0]
		long := runActualMainCase(t, "long", actualMainCase{numberedMeasurementSources(strings.Repeat("long", 4096), 2), 1, 2, false})[0]
		if !reflect.DeepEqual(short.Indices, long.Indices) || short.Words != long.Words || long.Freeze <= short.Freeze || long.B <= short.B || long.K <= short.K {
			t.Fatalf("same n/words did not preserve members and bill actual long bytes: short=%+v long=%+v", short, long)
		}
	})
	t.Run("same_words_unicode", func(t *testing.T) {
		ascii := runActualMainCase(t, "ascii", actualMainCase{numberedMeasurementSources("a", 2), 1, 2, false})[0]
		unicode := runActualMainCase(t, "unicode", actualMainCase{numberedMeasurementSources("猫", 2), 1, 2, false})[0]
		if ascii.Words != unicode.Words || !reflect.DeepEqual(ascii.Indices, unicode.Indices) || ascii.K == unicode.K || ascii.B == unicode.B {
			t.Fatalf("same-word Unicode actual-byte pair collapsed: ascii=%+v unicode=%+v", ascii, unicode)
		}
	})
	t.Run("same_words_json_escaping", func(t *testing.T) {
		plain := runActualMainCase(t, "plain", actualMainCase{numberedMeasurementSources("red blue", 2), 1, 2, false})[0]
		escaped := runActualMainCase(t, "escaped", actualMainCase{numberedMeasurementSources("red\\\"\nblue", 2), 1, 2, false})[0]
		if plain.Words != escaped.Words || !reflect.DeepEqual(plain.Indices, escaped.Indices) || escaped.Wire <= plain.Wire || escaped.B <= plain.B || escaped.K <= plain.K {
			t.Fatalf("same-word JSON escape pair did not bill actual encoded bytes: plain=%+v escaped=%+v", plain, escaped)
		}
	})
	t.Run("same_text_context", func(t *testing.T) {
		sources := numberedMeasurementSources("neighbor source", 4)
		without := runActualMainCase(t, "context_off", actualMainCase{sources, 1, 2, false})
		with := runActualMainCase(t, "context_on", actualMainCase{sources, 1, 2, true})
		for i := range without {
			if !reflect.DeepEqual(without[i].Targets, with[i].Targets) || !reflect.DeepEqual(without[i].Indices, with[i].Indices) || without[i].Words != with[i].Words || without[i].ContextMembers != 0 || with[i].ContextMembers == 0 || with[i].ContextUTF8 == 0 || with[i].B <= without[i].B || with[i].K <= without[i].K {
				t.Fatalf("same text / real neighbor context pair failed: off=%+v on=%+v", without[i], with[i])
			}
		}
	})
}

func TestResourceBudgetMainBatchIndependentOnlyC(t *testing.T) {
	sources := numberedMeasurementSources("fixed source", 12)
	serial := runActualMainCase(t, "fixed_C1", actualMainCase{sources, 1, 4, false})
	parallel := runActualMainCase(t, "fixed_C4", actualMainCase{sources, 4, 4, false})
	// All input, batch, S/W/Q/R/J, backend, and aux-disabled policy stay fixed.
	// UUID contents differ but have identical byte width; measured sizes and
	// membership must therefore agree per logical batch, not just in totals.
	if !reflect.DeepEqual(serial, parallel) {
		t.Fatalf("only C changed complete main batches or actual K/b: C1=%+v C4=%+v", serial, parallel)
	}
}

type actualAuxObservation struct {
	body         []byte
	system, user string
	release      chan struct{}
}

type actualAuxHTTP struct {
	fixture   *rubyPipelineFixture
	mu        sync.Mutex
	main, aux int
	mainBody  []byte
	observed  chan actualAuxObservation
	stop      chan struct{}
	once      sync.Once
}

func (h *actualAuxHTTP) open() { h.once.Do(func() { close(h.stop) }) }
func (h *actualAuxHTTP) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || r.ContentLength != int64(len(body)) {
		http.Error(w, "incomplete actual wire", http.StatusBadRequest)
		return
	}
	var wire struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	observation := actualAuxObservation{body: body, release: make(chan struct{})}
	for _, message := range wire.Messages {
		if message.Role == "system" {
			observation.system = message.Content
		}
		if message.Role == "user" {
			observation.user = message.Content
		}
	}
	var request rubyPipelineRequest
	if err := json.Unmarshal([]byte(observation.user), &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	main := request.Segments != nil
	h.mu.Lock()
	if main {
		h.main++
		h.mainBody = bytes.Clone(body)
	} else {
		h.aux++
	}
	h.mu.Unlock()
	if !main {
		if len(request.Missing) != 1 || len(request.Alignments) != 0 {
			http.Error(w, "fixture must have one actual aux member", http.StatusBadRequest)
			return
		}
		select {
		case h.observed <- observation:
		case <-r.Context().Done():
			return
		}
		select {
		case <-observation.release:
		case <-h.stop:
		case <-r.Context().Done():
			return
		}
	}
	// Forward the identical captured body to the existing real Ruby provider
	// fixture. Its legal response and actual usage retain their original codec.
	r.Body = io.NopCloser(bytes.NewReader(body))
	h.fixture.serveHTTP(w, r)
}

type actualAuxCaseResult struct {
	mainBody              []byte
	auxBody               []byte
	Wire, B, Candidate, I int64
	Source, Target        string
	Alignment             []byte
	main, aux             int
}

func captureActualAuxCandidate(t *testing.T, f *rubyPipelineFixture, q int64, result *actualAuxCaseResult) {
	t.Helper()
	row, err := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobIDEQ(f.jobID))).Only(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actual := int64(len(row.Payload))
	if actual <= 0 || actual > q || row.PayloadBytes != actual {
		t.Fatalf("actual candidate body is not within Q: actual%d declared%d Q%d", actual, row.PayloadBytes, q)
	}
	candidate, err := pipeline.DecodeCandidateBounded(row.Payload, q, 0)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Alignment == nil || candidate.Ready {
		t.Fatalf("fixture has no actual unfinished aux input: %+v", candidate)
	}
	result.Candidate = actual
	result.Source, result.Target = candidate.Segment.Source, candidate.Segment.Target
	result.Alignment, err = json.Marshal(candidate.Alignment)
	if err != nil {
		t.Fatal(err)
	}
}

func runActualAuxInputCase(t *testing.T, w int64, wantAux bool) actualAuxCaseResult {
	t.Helper()
	const q = int64(8 << 10)
	h := &actualAuxHTTP{observed: make(chan actualAuxObservation, 1), stop: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(server.Close)
	t.Cleanup(h.open)
	f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), server.URL, 1)
	h.fixture = f
	f.snapshot.Rounds[0].Translate.BatchSize = 1
	f.snapshot.Rounds[0].Translate.Concurrency = 1
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
	f.snapshot.RubyRetry.Concurrency = 1
	f.snapshot.RubyRetry.MaxAttempts = 1
	f.snapshot.Strategy.Context.Enabled = false
	f.snapshot.Strategy.QA.Enabled = false
	f.snapshot.Strategy.Postprocess.Enabled = false
	// Literal source/template are identical between cases. The main provider
	// leaves a single real ruby item missing, so the aux protocol is singleton.
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.snapshot.RubyTemplates.JSON = strings.Repeat("Alignment guidance. ", 1000) + f.snapshot.RubyTemplates.JSON
	f.persistSnapshot(t)
	f.client.Segment.UpdateOneID(f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(context.Background()).ID).SetSourceText("<ruby>source-a<rt>original-a</rt></ruby> short").ExecX(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	limits := pipeline.CandidateLimits{Segments: 4, Bytes: w, ItemBytes: q}
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, store := f.round(t, 0)
	eng.PrepareDocument(doc, nil)
	type outcome struct {
		result pipeline.RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
		done <- outcome{result, err}
	}()
	joined := false
	defer func() {
		h.open()
		cancel()
		if !joined {
			<-done
		}
	}()
	result := actualAuxCaseResult{I: max(w, q)}
	if wantAux {
		select {
		case observation := <-h.observed:
			result.auxBody = bytes.Clone(observation.body)
			result.Wire = int64(len(observation.body))
			result.B = result.Wire + int64(len(observation.system)) + int64(len(observation.user))
			if result.Wire <= q || result.Wire > result.I || result.B <= q || result.B > result.I {
				t.Fatalf("actual aux input is not Q<wire<=b<=I: Q%d wire%d b%d I%d", q, result.Wire, result.B, result.I)
			}
			captureActualAuxCandidate(t, f, q, &result)
			if snapshot := runtime.Admission.PipelineSnapshot(); snapshot.AuxOwners != 1 || snapshot.AuxInputBytes != result.B || snapshot.AuxResponseBytes != 1<<20 {
				t.Fatalf("actual aux HTTP footprint differs from granted input/response: b%d snapshot%+v", result.B, snapshot)
			}
			close(observation.release)
		case out := <-done:
			joined = true
			t.Fatalf("legal I stopped before the required actual aux HTTP: %+v %v", out.result, out.err)
		case <-ctx.Done():
			t.Fatal("legal I never reached actual aux HTTP")
		}
	}
	var out outcome
	select {
	case out = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("aux input case did not join")
	}
	h.mu.Lock()
	result.main, result.aux, result.mainBody = h.main, h.aux, bytes.Clone(h.mainBody)
	h.mu.Unlock()
	if result.main != 1 || result.aux != map[bool]int{true: 1, false: 0}[wantAux] {
		t.Fatalf("changed actual HTTP work: main%d aux%d allowed%v err%v", result.main, result.aux, wantAux, out.err)
	}
	if wantAux {
		if out.err != nil || len(out.result.Resolved) != 1 || len(out.result.Unresolved) != 0 {
			t.Fatalf("legal Q<aux<I round failed: %+v %v", out.result, out.err)
		}
		row := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(context.Background())
		if row.TargetText == nil || !strings.Contains(*row.TargetText, "<ruby>alpha<rt>reading-a</rt></ruby>") || row.Status != segment.StatusTranslated {
			t.Fatalf("aux input acceptance did not commit actual ruby result: %+v", row)
		}
		if f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(context.Background()) != 1 {
			t.Fatal("legal aux input lost checkpoint")
		}
		f.assertUsage(t, 2)
	} else {
		if !errors.Is(out.err, backend.ErrPipelineCapacity) || len(out.result.Resolved) != 0 || len(out.result.Unresolved) != 1 {
			t.Fatalf("I below fixed aux input did not refuse capacity: %+v %v", out.result, out.err)
		}
		captureActualAuxCandidate(t, f, q, &result)
		row := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(context.Background())
		if row.TargetText != nil || f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(context.Background()) != 0 {
			t.Fatal("undispatched aux half-confirmed the target/checkpoint")
		}
		member := f.client.WorkItem.Query().Where(workitem.JobIDEQ(f.jobID)).OnlyX(context.Background())
		if member.CandidateID == "" || member.AlignmentAttempts != 0 || member.AlignmentNetworkAttempts != 0 {
			t.Fatalf("I refusal lost candidate or paid an aux attempt: %+v", member)
		}
		f.assertUsage(t, 1)
	}
	if doc.InputTokens != int64(result.main+result.aux)*7 || doc.OutputTokens != int64(result.main+result.aux)*11 {
		t.Fatalf("actual main usage lost on aux capacity: doc%d/%d HTTP%d/%d", doc.InputTokens, doc.OutputTokens, result.main, result.aux)
	}
	if snapshot := runtime.Admission.PipelineSnapshot(); snapshot.AuxOwners != 0 || snapshot.AuxInputBytes != 0 || snapshot.AuxResponseBytes != 0 || snapshot.MainOwners[0] != 0 {
		t.Fatalf("joined aux input case retained network owners: %+v", snapshot)
	}
	t.Logf("Q=%d W=%d I=%d actual_candidate=%d actual_aux_wire=%d actual_aux_b=%d main_HTTP=%d aux_HTTP=%d actual_usage=%d/%d allowed=%v error=%v", q, w, result.I, result.Candidate, result.Wire, result.B, result.main, result.aux, doc.InputTokens, doc.OutputTokens, wantAux, out.err)
	return result
}

func TestResourceBudgetAuxActualWireMayExceedQWithinI(t *testing.T) {
	allowed := runActualAuxInputCase(t, 64<<10, true)
	refused := runActualAuxInputCase(t, 16<<10, false)
	// The denied request must never reach HTTP. The already-observed real wire
	// is its counterfactual only after proving exact main wire and the actual
	// source/target/alignment input are identical; neither response nor bytes
	// are generated after refusal to stand in for a real request measurement.
	if !bytes.Equal(allowed.mainBody, refused.mainBody) || allowed.Source != refused.Source || allowed.Target != refused.Target || !bytes.Equal(allowed.Alignment, refused.Alignment) {
		t.Fatalf("aux input pair changed more than W/I: allowed=%+v refused=%+v", allowed, refused)
	}
	if allowed.Wire <= refused.I || allowed.B <= refused.I {
		t.Fatalf("lowered I is not below the same observed complete aux request: wire%d b%d lowerI%d", allowed.Wire, allowed.B, refused.I)
	}
	if refused.aux != 0 || refused.main != 1 {
		t.Fatal("I refusal did not preserve one paid main and zero paid aux")
	}
	t.Logf("only_W_I_changed=true candidate<=Q=8192 allowed_actual_wire=%d allowed_actual_b=%d I_allowed=%d I_refused=%d refused_aux_HTTP=0 preserved_main_HTTP=1 preserved_main_usage=7/11", allowed.Wire, allowed.B, allowed.I, refused.I)
}
