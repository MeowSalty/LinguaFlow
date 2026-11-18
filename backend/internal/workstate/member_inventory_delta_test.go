package workstate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workresponsemember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

var memberDeltaLookupFailure = errors.New("fixture authoritative member lookup unavailable")

// The barrier is after the real SQLite COMMIT, before its caller can settle the
// touched rows. Query faults act at the real driver boundary rather than
// substituting a guessed inventory vector.
type memberDeltaFaults struct {
	loseACK              atomic.Bool
	failAfterCommit      atomic.Bool
	failLookup           atomic.Bool
	pauseCommit          atomic.Bool
	lookupFailures       atomic.Int64
	recordPayloadQueries atomic.Bool
	payloadQueries       atomic.Int64
	identityQueries      atomic.Int64
	entered              chan struct{}
	release              chan struct{}
	releaseOnce          sync.Once
}

type memberDeltaDriver struct {
	dialect.Driver
	faults *memberDeltaFaults
}
type memberDeltaTx struct {
	dialect.Tx
	faults *memberDeltaFaults
	ctx    context.Context
}

func (f *memberDeltaFaults) observeProjection(q string) {
	if !f.recordPayloadQueries.Load() || !strings.HasPrefix(q, "SELECT ") {
		return
	}
	selection := strings.SplitN(q, " FROM ", 2)[0]
	if strings.Contains(q, "work_request_input_members") {
		f.identityQueries.Add(1)
	}
	for _, field := range []string{"payload", "corrected_payload", "capture_payload", "response_glossary", "glossary_receipt"} {
		if strings.Contains(selection, ".`"+field+"`") || strings.Contains(selection, ".\""+field+"\"") {
			f.payloadQueries.Add(1)
			break
		}
	}
}

func (d memberDeltaDriver) Query(ctx context.Context, q string, args, value any) error {
	d.faults.observeProjection(q)
	return d.Driver.Query(ctx, q, args, value)
}

func (d memberDeltaDriver) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.Driver.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}).ExecContext(ctx, q, args...)
}

func (d memberDeltaDriver) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if d.faults.failLookup.Load() && strings.Contains(q, "WHERE id=") {
		d.faults.lookupFailures.Add(1)
		return nil, memberDeltaLookupFailure
	}
	return d.Driver.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}).QueryContext(ctx, q, args...)
}

func (d memberDeltaDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return memberDeltaTx{Tx: tx, faults: d.faults, ctx: ctx}, nil
}

func (tx memberDeltaTx) Query(ctx context.Context, q string, args, value any) error {
	tx.faults.observeProjection(q)
	if tx.faults.failLookup.Load() && strings.Contains(q, "work_requests") {
		tx.faults.lookupFailures.Add(1)
		return memberDeltaLookupFailure
	}
	return tx.Tx.Query(ctx, q, args, value)
}

func (tx memberDeltaTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if tx.faults.failLookup.Load() && strings.Contains(q, "WHERE id=") {
		tx.faults.lookupFailures.Add(1)
		return nil, memberDeltaLookupFailure
	}
	return tx.Tx.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}).QueryContext(ctx, q, args...)
}

func (tx memberDeltaTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.faults.pauseCommit.Swap(false) {
		close(tx.faults.entered)
		select {
		case <-tx.faults.release:
		case <-tx.ctx.Done():
			return tx.ctx.Err()
		}
	}
	if tx.faults.loseACK.Swap(false) {
		if tx.faults.failAfterCommit.Swap(false) {
			tx.faults.failLookup.Store(true)
		}
		return errors.New("fixture member COMMIT acknowledgement lost")
	}
	return nil
}

func memberDeltaFixture(t *testing.T) (context.Context, *ent.Client, *Store, Scope, *ent.Segment, *memberDeltaFaults) {
	t.Helper()
	ctx := t.Context()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "member-delta.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	faults := &memberDeltaFaults{entered: make(chan struct{}), release: make(chan struct{})}
	client := ent.NewClient(ent.Driver(memberDeltaDriver{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), faults: faults}))
	t.Cleanup(func() { faults.releaseOnce.Do(func() { close(faults.release) }); _ = client.Close(); _ = db.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, store, scope, first := seedFixture(t, ctx, client)
	return ctx, client, store, scope, first, faults
}

func memberDeltaBudget(t *testing.T, ctx context.Context, client *ent.Client, scope Scope) (*StorageBudget, context.Context) {
	t.Helper()
	actor := client.User.Create().SetUsername("member-delta").SetPasswordHash("fixture").SaveX(ctx)
	client.Job.UpdateOneID(scope.JobID).SetCreatedByID(actor.ID).ExecX(ctx)
	source := InventorySource{DB: client, Driver: dialect.SQLite, ActorID: actor.ID, ItemBytes: 1 << 20}
	root := integrationBudget(t)
	if err := root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := root.RebuildActor(ctx, actor.ID, source); err != nil {
		t.Fatal(err)
	}
	owner := resourcebudget.Scope{ActorID: actor.ID, JobID: scope.JobID}
	if err := root.Claim(owner, RuntimeIdentity(scope.JobID), resourcebudget.Vector{LogicalStorageBytes: 8 << 20}); err != nil {
		t.Fatal(err)
	}
	InstallStorageHooks(client)
	storage := &StorageBudget{Root: root, Source: source, Scope: owner}
	t.Cleanup(func() { _ = storage.Close() })
	return storage, WithStorageBudget(ctx, storage)
}

func memberDeltaAudit(t *testing.T, ctx context.Context, b *StorageBudget) int64 {
	t.Helper()
	_, actual, err := b.Source.Job(ctx, b.Scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	owned, ok := b.Root.Usage(b.Scope, StorageIdentity(b.Scope.JobID))
	physical := memberDeltaPhysicalStorage(t, ctx, b.Source.DB.(*ent.Client), b.Scope.JobID)
	if !ok || actual <= 0 || owned.LogicalStorageBytes != actual {
		t.Fatalf("physical durable audit differs from Root storage: Source=%d independent_physical=%d owned=%+v attached=%t", actual, physical, owned, ok)
	}
	if actual != physical {
		t.Fatalf("Source and Root disagree with independent physical SQL: Source=%d physical=%d", actual, physical)
	}
	return actual
}

// This oracle names physical fields independently of the production column
// lists and cached metadata expressions. A shared error in Source and Root
// cannot make the test pass by comparing those two formulas to each other.
func memberDeltaPhysicalStorage(t *testing.T, ctx context.Context, client *ent.Client, jobID int) int64 {
	t.Helper()
	queryScalar := func(query string) int64 {
		t.Helper()
		rows, err := client.QueryContext(ctx, query, jobID)
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		if err := (inventoryRow{rows: rows}).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	requestTerms := "COALESCE(LENGTH(CAST(members AS BLOB)),0)+COALESCE(LENGTH(CAST(debits AS BLOB)),0)+COALESCE(LENGTH(CAST(segment_ids AS BLOB)),0)+COALESCE(LENGTH(CAST(glossary_receipt AS BLOB)),0)+COALESCE(LENGTH(capture_payload),0)+COALESCE(LENGTH(response_glossary),0)"
	columns, err := client.QueryContext(ctx, "PRAGMA table_info(work_requests)")
	if err != nil {
		t.Fatal(err)
	}
	for columns.Next() {
		var id, required, primary int
		var name, kind string
		var value any
		if err := columns.Scan(&id, &name, &kind, &required, &value, &primary); err != nil {
			_ = columns.Close()
			t.Fatal(err)
		}
		switch name {
		case "freeze_payload", "response_payload", "corrected_targets", "corrected_target_index", "promotion_members", "promotion_status":
			requestTerms += "+COALESCE(LENGTH(CAST(" + name + " AS BLOB)),0)"
		}
	}
	if err := errors.Join(columns.Err(), columns.Close()); err != nil {
		t.Fatal(err)
	}
	metadataRows, err := client.QueryContext(ctx, "SELECT id,request_metadata_bytes,COALESCE(LENGTH(CAST(members AS BLOB)),0)+COALESCE(LENGTH(CAST(debits AS BLOB)),0)+COALESCE(LENGTH(CAST(segment_ids AS BLOB)),0) FROM work_requests WHERE job_id=? AND response_format_version=2", jobID)
	if err != nil {
		t.Fatal(err)
	}
	for metadataRows.Next() {
		var id int
		var stored, physical int64
		if err := metadataRows.Scan(&id, &stored, &physical); err != nil {
			_ = metadataRows.Close()
			t.Fatal(err)
		}
		if stored != physical {
			_ = metadataRows.Close()
			t.Fatalf("request=%d cached metadata=%d physical members/debits/segment_ids=%d", id, stored, physical)
		}
	}
	if err := errors.Join(metadataRows.Err(), metadataRows.Close()); err != nil {
		t.Fatal(err)
	}
	requests := queryScalar("SELECT COALESCE(SUM(" + requestTerms + "),0) FROM work_requests WHERE job_id=?")
	inputs := queryScalar("SELECT COALESCE(SUM(COALESCE(LENGTH(m.payload),0)+COALESCE(LENGTH(CAST(m.prompt_id AS BLOB)),0)+COALESCE(LENGTH(CAST(m.work_id AS BLOB)),0)+COALESCE(LENGTH(CAST(m.candidate_id AS BLOB)),0)+COALESCE(LENGTH(CAST(m.baseline_digest AS BLOB)),0)+COALESCE(LENGTH(CAST(m.baseline_status AS BLOB)),0)+COALESCE(LENGTH(CAST(m.payload_digest AS BLOB)),0)),0) FROM work_request_input_members m JOIN work_requests r ON r.id=m.request_id WHERE r.job_id=?")
	outputs := queryScalar("SELECT COALESCE(SUM(COALESCE(LENGTH(m.payload),0)+COALESCE(LENGTH(m.corrected_payload),0)+COALESCE(LENGTH(CAST(m.payload_digest AS BLOB)),0)+COALESCE(LENGTH(CAST(m.corrected_digest AS BLOB)),0)+COALESCE(LENGTH(CAST(m.failure AS BLOB)),0)+COALESCE(LENGTH(CAST(m.candidate_digest AS BLOB)),0)),0) FROM work_response_members m JOIN work_requests r ON r.id=m.request_id WHERE r.job_id=?")
	candidates := queryScalar("SELECT COALESCE(SUM(COALESCE(LENGTH(CAST(c.payload AS BLOB)),0)+COALESCE(LENGTH(c.pipeline_header),0)+COALESCE(LENGTH(CAST(c.baseline_target AS BLOB)),0)),0) FROM work_candidates c JOIN work_items w ON w.id=c.work_item_id WHERE w.job_id=?")
	return requests + inputs + outputs + candidates
}

func TestMemberInventoryDeltaConcurrentCorrectionsAndLateConfirmation(t *testing.T) {
	ctx, client, store, scope, first, faults := memberDeltaFixture(t)
	r, capture, candidates := integrityMemberPair(t, ctx, client, store, scope, first)
	b, owned := memberDeltaBudget(t, ctx, client, scope)
	if err := store.ReserveRequest(owned, r); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	faults.loseACK.Store(true)
	publication := integrityPublication(r, 0, 2, true)
	if err := store.PublishOutputs(owned, publication); err != nil {
		t.Fatal(err)
	}
	memberDeltaAudit(t, ctx, b)
	before := b.Root.Snapshot(b.Scope.ActorID).Instance
	if err := store.PublishOutputs(owned, publication); err != nil {
		t.Fatal(err)
	}
	if b.Root.Snapshot(b.Scope.ActorID).Instance != before {
		t.Fatal("duplicate output ACK recharged durable rows")
	}
	faults.pauseCommit.Store(true)
	done := make(chan error, 2)
	go func() { done <- store.StageCorrectedMember(owned, integrityOutput(r, 0, "first correction")) }()
	select {
	case <-faults.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first correction never reached real COMMIT")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		done <- store.StageCorrectedMember(owned, integrityOutput(r, 1, "second correction"))
	}()
	<-secondStarted
	select {
	case err := <-done:
		t.Fatalf("second writer passed an unsettled committed delta: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	faults.releaseOnce.Do(func() { close(faults.release) })
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("correction writer did not join")
		}
	}
	if client.WorkResponseMember.Query().Where(workresponsemember.CorrectedBytesGT(0)).CountX(ctx) != 2 {
		t.Fatal("concurrent writers lost a correction")
	}
	memberDeltaAudit(t, ctx, b)
	if err := store.PublishMemberBusiness(owned, r.ID, nil); err != nil {
		var receiptBytes int64
		rows, lookupErr := client.QueryContext(ctx, "SELECT COALESCE(LENGTH(CAST(glossary_receipt AS BLOB)),0) FROM work_requests WHERE identity=?", r.ID)
		if lookupErr == nil {
			lookupErr = inventoryRow{rows: rows}.Scan(&receiptBytes)
		}
		_, actual, auditErr := b.Source.Job(ctx, scope.JobID)
		charge, _ := b.Root.Usage(b.Scope, StorageIdentity(scope.JobID))
		t.Fatalf("empty receipt publication: %v; stored_receipt_bytes=%d lookup=%v actual_storage=%d audit=%v Root_storage=%d", err, receiptBytes, lookupErr, actual, auditErr, charge.LogicalStorageBytes)
	}
	for ordinal := range 2 {
		candidate := candidates[ordinal]
		// Promotion preserves the counters already debited by ReserveRequest;
		// a zero-value Next would illegally rewind its main attempt from 1 to 0.
		next, err := store.LoadCursor(ctx, scope, candidate.SegmentID)
		if err != nil {
			t.Fatal(err)
		}
		if next.MainAttempts != 1 {
			t.Fatalf("reservation has unexpected main debit: %+v", next)
		}
		if err := store.PromoteMember(owned, MemberPromotion{Scope: scope, Key: requestMemberKey(r, ordinal), Candidate: &candidate, Next: next}); err != nil {
			t.Fatalf("promotion ordinal=%d preserving reserved cursor: %v", ordinal, err)
		}
		memberDeltaAudit(t, ctx, b)
		commit := input(candidate)
		if ordinal == 1 {
			commit.CommitID = "commit-second"
		}
		if _, err := store.Commit(owned, commit); err != nil {
			t.Fatal(err)
		}
		memberDeltaAudit(t, ctx, b)
		before = b.Root.Snapshot(b.Scope.ActorID).Instance
		// A confirmation received after the candidate payload was retired must
		// use its durable commit identity, without refunding it a second time.
		result, err := store.Commit(owned, commit)
		if err != nil || result.Outcome != AlreadyCommitted || b.Root.Snapshot(b.Scope.ActorID).Instance != before {
			t.Fatalf("late confirmation recharged/refunded storage: %+v %v", result, err)
		}
		final, err := store.LoadCursor(ctx, scope, candidate.SegmentID)
		if err != nil || final.MainAttempts != next.MainAttempts || final.AlignmentAttempts != next.AlignmentAttempts || final.NetworkAttempts != next.NetworkAttempts || final.MainNetworkAttempts != next.MainNetworkAttempts || final.AlignmentNetworkAttempts != next.AlignmentNetworkAttempts {
			t.Fatalf("late confirmation changed reserved attempt counters: before=%+v after=%+v err=%v", next, final, err)
		}
	}
	row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx)
	if row.PromotionCursor != 2 || row.RemainingMembers != 0 || row.InputMemberBytes+row.OutputMemberBytes+row.CaptureBytes != 0 || client.JobRoundSegment.Query().CountX(ctx) != 2 || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("member lifecycle lost final durable identity/counts")
	}
	memberDeltaAudit(t, ctx, b)
}

func TestMemberInventoryDeltaUnknownACKLookupFailureKeepsCharge(t *testing.T) {
	for _, recovery := range []string{"cold_recovery", "same_attempt"} {
		t.Run(recovery, func(t *testing.T) { memberDeltaUnknownACKRecovery(t, recovery == "same_attempt") })
	}
}

func memberDeltaUnknownACKRecovery(t *testing.T, sameAttempt bool) {
	t.Helper()
	ctx, client, store, scope, first, faults := memberDeltaFixture(t)
	r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
	b, owned := memberDeltaBudget(t, ctx, client, scope)
	if err := store.ReserveRequest(owned, r); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	memberDeltaAudit(t, ctx, b)
	faults.loseACK.Store(true)
	faults.failAfterCommit.Store(true)
	publication := integrityPublication(r, 0, 2, true)
	err := store.PublishOutputs(owned, publication)
	var unknown *VerificationError
	if !errors.As(err, &unknown) || !errors.Is(err, memberDeltaLookupFailure) || faults.lookupFailures.Load() == 0 {
		t.Fatalf("unavailable authority was treated as known: %v", err)
	}
	charged, ok := b.Root.Usage(b.Scope, StorageIdentity(scope.JobID))
	if !ok {
		t.Fatal("unknown durable publication was refunded")
	}
	faults.failLookup.Store(false)
	_, actual, err := b.Source.Job(ctx, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if charged.LogicalStorageBytes < actual {
		t.Fatalf("failed lookup refunded real committed backing: charged=%d actual=%d", charged.LogicalStorageBytes, actual)
	}
	row := client.WorkRequest.Query().OnlyX(ctx)
	if row.ArchiveCursor != 2 || row.PromotionState != "outputs_ready" || row.CaptureBytes != 0 || client.WorkResponseMember.Query().CountX(ctx) != 2 {
		t.Fatal("unknown ACK did not preserve the actual committed publication")
	}
	if sameAttempt {
		// The fact read is available again in the very same live attempt. Its
		// already committed touched-row delta must settle before a duplicate
		// publication can discard the attempt's pending inventory evidence.
		if err := store.PublishOutputs(owned, publication); err != nil {
			t.Fatal(err)
		}
		settled := memberDeltaAudit(t, ctx, b)
		if settled != actual {
			t.Fatalf("same-attempt confirmation changed durable bytes: before=%d after=%d", actual, settled)
		}
		before := b.Root.Snapshot(b.Scope.ActorID).Instance
		if err := store.PublishOutputs(owned, publication); err != nil {
			t.Fatal(err)
		}
		memberDeltaAudit(t, ctx, b)
		if b.Root.Snapshot(b.Scope.ActorID).Instance != before || client.WorkResponseMember.Query().CountX(ctx) != 2 || client.UsageRecord.Query().CountX(ctx) != 1 {
			t.Fatal("same-attempt duplicate ACK multiplied charge, member or usage")
		}
		return
	}
	// Recovery rebuilds from physical SQL facts with no old attempt's credit.
	// The duplicate late publication must subsequently be an exact no-op.
	restarted := integrationBudget(t)
	if err := restarted.Rebuild(ctx, b.Source); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RebuildActor(ctx, b.Scope.ActorID, b.Source); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Attach(b.Scope, StorageIdentity(scope.JobID), resourcebudget.Vector{LogicalStorageBytes: actual}); err != nil {
		t.Fatal(err)
	}
	recovered := &StorageBudget{Root: restarted, Source: b.Source, Scope: b.Scope}
	t.Cleanup(func() { _ = recovered.Close() })
	before := restarted.Snapshot(b.Scope.ActorID).Instance
	if err := NewStore(client).PublishOutputs(WithStorageBudget(ctx, recovered), publication); err != nil {
		t.Fatal(err)
	}
	memberDeltaAudit(t, ctx, recovered)
	if restarted.Snapshot(b.Scope.ActorID).Instance != before || client.WorkResponseMember.Query().CountX(ctx) != 2 || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("late ACK recovery duplicated charge, member or model usage")
	}
}

// Legacy columns may physically remain in an existing database even though
// the final ORM schema no longer exposes them. They are durable bytes, but are
// neither frozen inputs nor outputs of the current format2 invocation.
func TestMemberInventoryDeltaCountsLegacyPhysicalColumnsWithoutExecution(t *testing.T) {
	ctx, client, store, scope, first, _ := memberDeltaFixture(t)
	currentExecution(t, ctx, client, scope)
	r, capture, candidate := rawRequest(scope, first)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	legacyColumns := []string{"freeze_payload", "response_payload", "corrected_targets", "corrected_target_index", "promotion_members", "promotion_status"}
	var legacyBytes int64
	var lengthExpressions []string
	for _, column := range legacyColumns {
		// All identifiers come from the fixed schema list above; these are tiny
		// task-owned values in t.TempDir, never existing user data.
		if _, err := client.ExecContext(ctx, "ALTER TABLE work_requests ADD COLUMN "+column+" BLOB"); err != nil {
			t.Fatal(err)
		}
		payload := []byte("旧:" + column)
		if _, err := client.ExecContext(ctx, "UPDATE work_requests SET "+column+"=? WHERE identity=?", payload, r.ID); err != nil {
			t.Fatal(err)
		}
		legacyBytes += int64(len(payload))
		lengthExpressions = append(lengthExpressions, "COALESCE(LENGTH(CAST("+column+" AS BLOB)),0)")
	}
	// Column discovery and Root calibration happen after physical columns
	// exist, so no earlier schema-probe cache can hide their actual storage.
	b, owned := memberDeltaBudget(t, ctx, client, scope)
	_, initialStorage, err := b.Source.Job(ctx, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Root.Attach(b.Scope, StorageIdentity(scope.JobID), resourcebudget.Vector{LogicalStorageBytes: initialStorage}); err != nil {
		t.Fatal(err)
	}
	fields, err := b.Source.requestStorageFields(ctx)
	if err != nil {
		t.Fatal(err)
	}
	withoutLegacy := b.Source
	withoutLegacy.Columns = make(map[string]bool, len(fields))
	for _, field := range fields {
		old := false
		for _, column := range legacyColumns {
			if field == column {
				old = true
				break
			}
		}
		if !old {
			withoutLegacy.Columns[field] = true
		}
	}
	check := func() {
		t.Helper()
		actual := memberDeltaAudit(t, ctx, b)
		credit, _ := b.Root.Usage(b.Scope, RuntimeIdentity(scope.JobID))
		if b.Root.Snapshot(b.Scope.ActorID).Instance.LogicalStorageBytes-credit.LogicalStorageBytes != actual {
			t.Fatal("Root durable storage differs from actual SQL after excluding live save credit")
		}
		_, currentColumnsBytes, err := withoutLegacy.Job(ctx, scope.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if actual-currentColumnsBytes != legacyBytes {
			t.Fatalf("physical legacy bytes omitted/doubled: total=%d current_only=%d legacy=%d", actual, currentColumnsBytes, legacyBytes)
		}
		rows, err := client.QueryContext(ctx, "SELECT "+strings.Join(lengthExpressions, "+")+" FROM work_requests WHERE identity=?", r.ID)
		if err != nil {
			t.Fatal(err)
		}
		var physical int64
		if err := (inventoryRow{rows: rows}).Scan(&physical); err != nil {
			t.Fatal(err)
		}
		if physical != legacyBytes {
			t.Fatalf("current member execution altered legacy physical facts: physical=%d expected=%d", physical, legacyBytes)
		}
	}
	check()
	var hotFullJob atomic.Int64
	recorded := backend.WithMemberWorkRecorder(owned, func(e backend.MemberWorkEvent) {
		if e.FullJob {
			hotFullJob.Add(1)
		}
	})
	if err := store.CaptureResponse(recorded, capture); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishOutputs(recorded, integrityPublication(r, 0, 1, true)); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishMemberBusiness(recorded, r.ID, nil); err != nil {
		t.Fatal(err)
	}
	next, err := store.LoadCursor(ctx, scope, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteMember(recorded, MemberPromotion{Scope: scope, Key: requestMemberKey(r, 0), Candidate: &candidate, Next: next}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(recorded, input(candidate)); err != nil {
		t.Fatal(err)
	}
	if err := b.Root.Transfer(b.Scope, RuntimeIdentity(scope.JobID), resourcebudget.Vector{}); err != nil {
		t.Fatal(err)
	}
	check()
	if hotFullJob.Load() != 0 {
		t.Fatalf("member delta rescanned whole job: %d", hotFullJob.Load())
	}
	row := client.WorkRequest.Query().OnlyX(ctx)
	if row.ResponseFormatVersion != 2 || row.PromotionCursor != 1 || row.InputMemberBytes+row.OutputMemberBytes+row.CaptureBytes != 0 || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("physical inventory probe changed the final format2 execution contract")
	}
	t.Logf("legacy_physical_columns=%d initial/final_exact_legacy_bytes=%d hot_full_job=0 format=2", len(legacyColumns), legacyBytes)
}

func TestMemberInventoryDeltaClosedWriterCannotReviveDurableOwner(t *testing.T) {
	for _, mode := range []string{"settled_close", "failed_pending_close_then_retry"} {
		t.Run(mode, func(t *testing.T) {
			ctx, client, store, scope, first, faults := memberDeltaFixture(t)
			r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
			b, owned := memberDeltaBudget(t, ctx, client, scope)
			if err := store.ReserveRequest(owned, r); err != nil {
				t.Fatal(err)
			}
			if err := store.CaptureResponse(owned, capture); err != nil {
				t.Fatal(err)
			}
			publication := integrityPublication(r, 0, 2, true)
			if mode == "failed_pending_close_then_retry" {
				faults.loseACK.Store(true)
				faults.failAfterCommit.Store(true)
				if err := store.PublishOutputs(owned, publication); !errors.Is(err, memberDeltaLookupFailure) {
					t.Fatalf("missing pending query failure: %v", err)
				}
				before := b.Root.Snapshot(b.Scope.ActorID).Instance
				if err := b.Close(); !errors.Is(err, memberDeltaLookupFailure) {
					t.Fatalf("unverified pending close succeeded: %v", err)
				}
				if _, attached := b.Root.Usage(b.Scope, StorageIdentity(scope.JobID)); !attached || b.Root.Snapshot(b.Scope.ActorID).Instance != before {
					t.Fatal("failed pending close detached or refunded uncertain backing")
				}
				faults.failLookup.Store(false)
			} else if err := store.PublishOutputs(owned, publication); err != nil {
				t.Fatal(err)
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			if _, attached := b.Root.Usage(b.Scope, StorageIdentity(scope.JobID)); attached {
				t.Fatal("successful close kept old writer attached")
			}
			before := b.Root.Snapshot(b.Scope.ActorID).Instance
			if err := b.Close(); err != nil || b.Root.Snapshot(b.Scope.ActorID).Instance != before {
				t.Fatalf("repeated close changed durable ownership: %v", err)
			}
			err := store.PublishOutputs(owned, publication)
			var rejected *backend.RejectionError
			if !errors.Is(err, resourcebudget.ErrIdentity) || !errors.As(err, &rejected) || rejected.Evidence.Branch != "storage_budget_closed" {
				t.Fatalf("closed writer lost typed rejection: %v", err)
			}
			if _, attached := b.Root.Usage(b.Scope, StorageIdentity(scope.JobID)); attached || b.Root.Snapshot(b.Scope.ActorID).Instance != before {
				t.Fatal("late duplicate ACK revived closed writer or changed durable charge")
			}
			fresh := &StorageBudget{Root: b.Root, Source: b.Source, Scope: b.Scope}
			t.Cleanup(func() { _ = fresh.Close() })
			if err := NewStore(client).PublishOutputs(WithStorageBudget(ctx, fresh), publication); err != nil {
				t.Fatalf("fresh writer could not adopt unchanged durable facts: %v", err)
			}
			memberDeltaAudit(t, ctx, fresh)
			if b.Root.Snapshot(b.Scope.ActorID).Instance != before || client.WorkResponseMember.Query().CountX(ctx) != 2 || client.UsageRecord.Query().CountX(ctx) != 1 {
				t.Fatal("fresh writer duplicate multiplied charge, members or usage")
			}
			row := client.WorkRequest.Query().OnlyX(ctx)
			if row.ArchiveCursor != 2 || row.PromotionCursor != 0 || row.CaptureBytes != 0 {
				t.Fatal("close/reopen rewrote reliable publication progress")
			}
		})
	}
}
