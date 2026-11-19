package worker

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser/epub"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// The dense case reproduces the user's smallest failing batch. The larger
// synthetic book deliberately reaches the public word max before S; it is not
// a copy of the user's unavailable 5,338-segment source book.
func TestAllocationEPUBWorkerEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		segments, words, batch int
	}{
		{"dense_256_seven_keys_512_words", 256, 2, 256},
		{"synthetic_book_5338_word_bounded_batches", 5338, 20, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
			defer cancel()
			original := allocationEPUB(t, tc.segments, tc.words)
			parser := epub.New()
			doc, err := parser.Parse(ctx, bytes.NewReader(original), "epub")
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Segments) != tc.segments {
				t.Fatalf("parser members=%d want=%d", len(doc.Segments), tc.segments)
			}
			for _, seg := range doc.Segments {
				if len(seg.Meta) != 7 || len(strings.Fields(seg.Source)) != tc.words {
					t.Fatalf("real parser shape: meta=%v source=%q", seg.Meta, seg.Source)
				}
			}

			f := newRubyPipelineFixtureWithClient(t, allocationSQLite(t), "", 0)
			resourceID := f.resourceIDs[0]
			f.client.Resource.UpdateOneID(resourceID).SetFormat("epub").SetTotalSegments(tc.segments).ExecX(ctx)
			f.client.JobResource.UpdateOneID(f.jobResources[0]).SetStatus(service.JobResourceStatusPending).SetSegmentCount(tc.segments).ExecX(ctx)
			for start := 0; start < len(doc.Segments); start += 100 {
				builders := make([]*ent.SegmentCreate, 0, min(100, len(doc.Segments)-start))
				for i := start; i < min(start+100, len(doc.Segments)); i++ {
					meta, err := json.Marshal(doc.Segments[i].Meta)
					if err != nil {
						t.Fatal(err)
					}
					builders = append(builders, f.client.Segment.Create().SetResourceID(resourceID).SetSegmentIndex(i).SetSourceText(doc.Segments[i].Source).SetMeta(string(meta)))
				}
				f.client.Segment.CreateBulk(builders...).ExecX(ctx)
			}
			// End the upload-side ownership before the worker performs its real
			// bounded database projection and metadata decode.
			clear(doc.Segments)
			doc = nil
			translate := f.snapshot.Rounds[0].Translate
			translate.BatchSize, translate.MaxWordsPerBatch, translate.Concurrency = 0, 4000, 1
			f.snapshot.Strategy.Ruby.Enabled = false
			f.snapshot.Strategy.Context.Enabled = false
			f.snapshot.RubyRetry = nil
			// Resolve the already complete snapshot directly: the generic fixture
			// helper fills a missing batch size with 10 and cannot represent the
			// deliberate public zero required by this regression.
			f.snapshot, err = execution.Resolve(*f.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			f.persistSnapshot(t)
			if f.runner.pipeCfg.Candidates.Segments != 256 {
				t.Fatalf("fixture changed default S: %+v", f.runner.pipeCfg.Candidates)
			}
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			source := workstate.InventorySource{DB: f.client, Driver: "sqlite", ItemBytes: f.runner.pipeCfg.Candidates.ItemBytes}
			f.runner.SetRootBudget(root, nil)
			f.runner.SetBudgetSource(source, nil)
			workstate.InstallStorageHooks(f.client)
			observed := &allocationHTTPObserver{}
			clients := telemetry.NewHTTPClients(nil, observed)
			t.Cleanup(clients.Shutdown)
			f.runner.httpClients = []telemetry.HTTPClientFactory{clients}
			var batchMembers []int
			var rootSamples []int64
			var samplesMu sync.Mutex
			var peakRoot int64
			f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
				members := 0
				for _, seg := range request.Segments {
					if seg.Translate {
						members++
					}
				}
				if members == 0 || len(request.Missing) != 0 || len(request.Alignments) != 0 {
					return nil, fmt.Errorf("unexpected auxiliary/empty call")
				}
				v := root.Snapshot(f.ownerID).Instance
				if v.PayloadBytes <= 0 || v.MainResponseOwners != 1 {
					return nil, fmt.Errorf("dispatch has no finite payload/response owner: %+v", v)
				}
				samplesMu.Lock()
				batchMembers = append(batchMembers, members)
				rootSamples = append(rootSamples, v.PayloadBytes)
				peakRoot = max(peakRoot, v.PayloadBytes)
				samplesMu.Unlock()
				return rubyPipelineResponse(request), nil
			}
			if err := f.runner.processJob(ctx, f.jobID); err != nil {
				facts, finish := context.WithTimeout(t.Context(), 5*time.Second)
				defer finish()
				confirmed, factErr := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).Count(facts)
				samplesMu.Lock()
				t.Logf("failure progress confirmed=%d/%d fact_error=%v HTTP_members=%v root_samples=%v", confirmed, tc.segments, factErr, batchMembers, rootSamples)
				samplesMu.Unlock()
				t.Fatal(err)
			}
			samplesMu.Lock()
			defer samplesMu.Unlock()
			job := f.client.Job.GetX(ctx, f.jobID)
			if job.Status != service.JobStatusCompleted {
				t.Fatalf("job status=%s error=%v", job.Status, job.ErrorMessage)
			}
			calls := (tc.segments + tc.batch - 1) / tc.batch
			if len(batchMembers) != calls {
				t.Fatalf("HTTP batches=%v want calls=%d", batchMembers, calls)
			}
			for i, members := range batchMembers {
				want := min(tc.batch, tc.segments-i*tc.batch)
				if members != want {
					t.Fatalf("original public batch %d cropped: %d want=%d", i, members, want)
				}
			}
			f.assertUsage(t, calls)
			rows := f.client.Segment.Query().Where(segment.ResourceIDEQ(resourceID)).Order(ent.Asc(segment.FieldSegmentIndex)).AllX(ctx)
			for _, row := range rows {
				if row.TargetText == nil || *row.TargetText != "alpha beta" || row.Status != segment.StatusTranslated || row.ContentVersion != 2 {
					t.Fatalf("incorrect commit: %+v", row)
				}
			}
			if n := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(ctx); n != tc.segments {
				t.Fatalf("confirmed=%d want=%d", n, tc.segments)
			}
			round := f.client.JobRound.GetX(ctx, f.roundIDs[0])
			if round.SegmentTotal != tc.segments || round.SegmentCompleted != tc.segments {
				t.Fatalf("progress=%d/%d", round.SegmentCompleted, round.SegmentTotal)
			}
			for _, request := range f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).AllX(ctx) {
				if request.State == "cancelled_before_dispatch" {
					continue
				}
				if request.State != "completed" || request.SnapshotDigest == "" || request.PromotionCursor != request.PromotionCount || request.PromotionCount == 0 || (request.CaptureBytes+request.OutputMemberBytes) != 0 || request.InputMemberBytes != 0 {
					t.Fatalf("freeze/response/promotion did not drain: %+v", request)
				}
			}
			v, storage, err := source.Job(ctx, f.jobID)
			if err != nil {
				t.Fatal(err)
			}
			if v != (resourcebudget.Vector{}) {
				t.Fatalf("completed job still owns execution inventory: %+v", v)
			}
			v.LogicalStorageBytes = storage
			if root.Snapshot(f.ownerID).Instance != v {
				t.Fatalf("joined Root leaked: root=%+v audit=%+v", root.Snapshot(f.ownerID).Instance, v)
			}
			observed.mu.Lock()
			admission := observed.admission
			observed.mu.Unlock()
			if admission == nil {
				t.Fatal("real HTTP has no admission observer")
			}
			if err := verifyHandoffOwnedTerminal(admission.OwnedPayloadSnapshot(), "freeze", "raw", "wire", "candidate"); err != nil {
				t.Fatal(err)
			}

			outputDoc := pipeline.BuildDocumentFromSegments(buildSegmentInputs(rows), "en", "zh", "epub")
			// A later batch can retain only the already adopted document fields
			// in addition to the first same-size request. Measure those concrete
			// final fields independently; this fails if each released response or
			// short build keeps its credit until the entire book finishes.
			var documentGrowth int64
			for _, seg := range outputDoc.Segments {
				for _, field := range []any{seg.Target, seg.Issues, seg.Meta, seg.Protected} {
					n, err := backend.ValueAllocationBytes(field)
					if err != nil {
						t.Fatal(err)
					}
					documentGrowth, err = backend.CheckedAdd(documentGrowth, n)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			allowed, err := backend.CheckedAdd(rootSamples[0], documentGrowth)
			if err != nil {
				t.Fatal(err)
			}
			if peakRoot > allowed {
				t.Fatalf("released batches accumulated: HTTP roots=%v first=%d final document backing=%d", rootSamples, rootSamples[0], documentGrowth)
			}
			var output bytes.Buffer
			if err := parser.Render(ctx, outputDoc, bytes.NewReader(original), &output); err != nil {
				t.Fatal(err)
			}
			rendered, err := parser.Parse(ctx, bytes.NewReader(output.Bytes()), "epub")
			if err != nil {
				t.Fatal(err)
			}
			if len(rendered.Segments) != tc.segments {
				t.Fatalf("output members=%d", len(rendered.Segments))
			}
			for _, seg := range rendered.Segments {
				if seg.Source != "alpha beta" {
					t.Fatalf("output lost translation: %q", seg.Source)
				}
			}
			t.Logf("real EPUB parser/SQLite/worker/HTTP/freeze/promotion/commit/output: segments=%d words=%d S=256 batches=%v peak_root_at_HTTP=%d joined_execution_roots=0 durable_audit_bytes=%d owned_backing_peak=%d", tc.segments, tc.segments*tc.words, batchMembers, peakRoot, storage, admission.OwnedPayloadSnapshot().TotalPeak)
		})
	}
}

type allocationHTTPObserver struct {
	mu        sync.Mutex
	admission *backend.RequestAdmission
}

func (o *allocationHTTPObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	if admission, ok := backend.OwnedObserverFromContext(request.Context()).(*backend.RequestAdmission); ok {
		o.mu.Lock()
		o.admission = admission
		o.mu.Unlock()
	}
	return http.DefaultTransport.RoundTrip(request)
}

func allocationSQLite(t *testing.T) *ent.Client {
	t.Helper()
	// Match persistenceacceptance/fixture.go WAL and the database subprocess
	// NORMAL setting, avoiding a full fsync per test transaction.
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "allocation-epub.db"))+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close(); _ = db.Close() })
	if err := client.Schema.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	return client
}

func allocationEPUB(t *testing.T, segments, words int) []byte {
	t.Helper()
	var body strings.Builder
	for i := range segments {
		fmt.Fprintf(&body, "<p>%ssegment%06d</p>\n", strings.Repeat("ordinary ", words-1), i)
	}
	files := []struct{ name, content string }{
		{"mimetype", "application/epub+zip"},
		{"META-INF/container.xml", `<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"OEBPS/content.opf", `<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Allocation synthetic book</dc:title></metadata><manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="chapter"/></spine></package>`},
		{"OEBPS/chapter.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Ordinary chapter</title></head><body>` + body.String() + `</body></html>`},
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, file := range files {
		method := uint16(zip.Deflate)
		if file.name == "mimetype" {
			method = zip.Store
		}
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: file.name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, file.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
