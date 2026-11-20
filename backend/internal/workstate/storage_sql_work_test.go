package workstate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"modernc.org/sqlite"
)

// Observe the real database/sql -> modernc boundary, including rows actually
// read. modernc QueryContext prepares internally; PrepareContext is a separate
// external API count and is never presented as an engine prepare count.
type storageSQLCall struct {
	query                       string
	kind                        string
	rows, valueBytes, textBytes int64
}
type storageSQLProbe struct {
	mu                        sync.Mutex
	enabled                   bool
	calls                     []*storageSQLCall
	begins, commits, prepares int
}

func (p *storageSQLProbe) start(kind, query string) *storageSQLCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return nil
	}
	call := &storageSQLCall{query: query, kind: kind}
	p.calls = append(p.calls, call)
	return call
}
func (p *storageSQLProbe) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enabled, p.calls, p.begins, p.commits, p.prepares = true, nil, 0, 0, 0
}

type storageSQLSnapshot struct {
	calls                                            []storageSQLCall
	queries, queryBytes, rows, valueBytes, textBytes int64
	begins, commits, prepares                        int
}

func (p *storageSQLProbe) stop() storageSQLSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enabled = false
	s := storageSQLSnapshot{begins: p.begins, commits: p.commits, prepares: p.prepares}
	for _, c := range p.calls {
		s.calls = append(s.calls, *c)
		if c.kind == "query" {
			s.queries++
			s.queryBytes += int64(len(c.query))
		}
		s.rows += c.rows
		s.valueBytes += c.valueBytes
		s.textBytes += c.textBytes
	}
	return s
}
func (s storageSQLSnapshot) identityReads() (combined, separate int) {
	for _, c := range s.calls {
		if c.kind != "query" || !strings.Contains(c.query, " FROM work_requests WHERE id=") {
			continue
		}
		projection := strings.SplitN(c.query, " FROM ", 2)[0]
		if strings.HasSuffix(projection, ",identity") {
			if strings.Contains(projection, "request_metadata_bytes") {
				combined++
			} else {
				separate++
			}
		}
	}
	return
}

type storageSQLConnector struct {
	name   string
	probe  *storageSQLProbe
	sqlite driver.Driver
}

func (c storageSQLConnector) Driver() driver.Driver { return c.sqlite }
func (c storageSQLConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.sqlite.Open(c.name)
	if err != nil {
		return nil, err
	}
	return &storageSQLConn{Conn: conn, probe: c.probe}, nil
}

type storageSQLConn struct {
	driver.Conn
	probe *storageSQLProbe
}

func (c *storageSQLConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}
func (c *storageSQLConn) IsValid() bool { return c.Conn.(driver.Validator).IsValid() }
func (c *storageSQLConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	call := c.probe.start("query", q)
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return &storageSQLRows{Rows: rows, probe: c.probe, call: call}, nil
}
func (c *storageSQLConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.probe.start("exec", q)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *storageSQLConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	c.probe.mu.Lock()
	if c.probe.enabled {
		c.probe.prepares++
	}
	c.probe.mu.Unlock()
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}
func (c *storageSQLConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	c.probe.mu.Lock()
	if c.probe.enabled {
		c.probe.begins++
	}
	c.probe.mu.Unlock()
	return storageSQLTx{Tx: tx, probe: c.probe}, nil
}

type storageSQLTx struct {
	driver.Tx
	probe *storageSQLProbe
}

func (tx storageSQLTx) Commit() error {
	err := tx.Tx.Commit()
	tx.probe.mu.Lock()
	if tx.probe.enabled && err == nil {
		tx.probe.commits++
	}
	tx.probe.mu.Unlock()
	return err
}

type storageSQLRows struct {
	driver.Rows
	probe *storageSQLProbe
	call  *storageSQLCall
}

func (r *storageSQLRows) Next(values []driver.Value) error {
	err := r.Rows.Next(values)
	if err != nil || r.call == nil {
		return err
	}
	r.probe.mu.Lock()
	defer r.probe.mu.Unlock()
	r.call.rows++
	for _, value := range values {
		switch value := value.(type) {
		case string:
			r.call.textBytes += int64(len(value))
			r.call.valueBytes += int64(len(value))
		case []byte:
			r.call.textBytes += int64(len(value))
			r.call.valueBytes += int64(len(value))
		case int64, float64:
			r.call.valueBytes += 8
		case bool:
			r.call.valueBytes++
		}
	}
	return nil
}

type storageSQLCreditProbe struct {
	*storagePendingCreditProbe
	uncertain, metadataFacts int
}

func (p *storageSQLCreditProbe) MarkRequestMetadataUncertain(string) { p.uncertain++ }
func (p *storageSQLCreditProbe) SetRequestMetadataBytes(string, int, int64) error {
	p.metadataFacts++
	return nil
}

func storageSQLFixture(t *testing.T) (context.Context, *ent.Client, *StorageBudget, context.Context, *ent.WorkRequest, *storageSQLProbe, *storageSQLCreditProbe, *memberDeltaFaults) {
	t.Helper()
	ctx := t.Context()
	probe := &storageSQLProbe{}
	db := sql.OpenDB(storageSQLConnector{name: filepath.Join(t.TempDir(), "storage-sql.db") + "?_pragma=foreign_keys(1)", probe: probe, sqlite: &sqlite.Driver{}})
	db.SetMaxOpenConns(1)
	faults := &memberDeltaFaults{entered: make(chan struct{}), release: make(chan struct{})}
	client := ent.NewClient(ent.Driver(memberDeltaDriver{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), faults: faults}))
	t.Cleanup(func() { faults.releaseOnce.Do(func() { close(faults.release) }); _ = client.Close(); _ = db.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, store, scope, first := seedFixture(t, ctx, client)
	b, owned := memberDeltaBudget(t, ctx, client, scope)
	credit := &storageSQLCreditProbe{storagePendingCreditProbe: &storagePendingCreditProbe{budget: b, remaining: 8 << 20}}
	b.Credit = credit
	r, _, _ := rawRequest(scope, first)
	if err := store.ReserveRequest(owned, r); err != nil {
		t.Fatal(err)
	}
	row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx)
	return ctx, client, b, owned, row, probe, credit, faults
}

func storageSQLUpdate(t *testing.T, client *ent.Client, b *StorageBudget, probe *storageSQLProbe, credit *storageSQLCreditProbe, ctx context.Context, fn func(*ent.Client) error) (storageSQLSnapshot, error) {
	t.Helper()
	uncertain, facts := credit.uncertain, credit.metadataFacts
	probe.reset()
	err := Transaction(ctx, client, func(tx *ent.Client) error {
		if err := fn(tx); err != nil {
			return err
		}
		if len(b.touched) != 1 || credit.uncertain != uncertain+1 {
			t.Error("write lost touched-row metadata uncertainty")
		}
		return nil
	})
	s := probe.stop()
	if credit.metadataFacts != facts+1 || len(b.touched) != 0 {
		t.Error("settlement omitted actual request metadata facts")
	}
	if s.begins != 1 || s.commits != 1 {
		t.Errorf("actual SQL transactions: begin=%d commit=%d", s.begins, s.commits)
	}
	if s.prepares != 0 {
		t.Errorf("modernc direct QueryContext unexpectedly used external PrepareContext: %d", s.prepares)
	}
	return s, err
}

func TestStorageBeforeIdentitySQLWork(t *testing.T) {
	t.Run("same_transaction_projection", func(t *testing.T) {
		ctx, client, b, owned, row, probe, credit, _ := storageSQLFixture(t)
		before := memberDeltaAudit(t, ctx, b)
		write := func(writeCtx context.Context) func(*ent.Client) error {
			return func(tx *ent.Client) error {
				return tx.WorkRequest.UpdateOneID(row.ID).SetPromotionCursor(0).Exec(writeCtx)
			}
		}
		baseline, err := storageSQLUpdate(t, client, b, probe, credit, owned, write(owned))
		if err != nil {
			t.Fatal(err)
		}
		trusted := persistence.WithMetadata(owned, persistence.Metadata{Operation: "scalar_update", RequestID: row.Identity})
		optimized, err := storageSQLUpdate(t, client, b, probe, credit, trusted, write(trusted))
		if err != nil {
			t.Fatal(err)
		}
		bc, bs := baseline.identityReads()
		oc, os := optimized.identityReads()
		if bc != 0 || bs != 1 || oc != 1 || os != 0 {
			t.Fatalf("actual before/identity SQL: baseline=%d/%d optimized=%d/%d", bc, bs, oc, os)
		}
		if baseline.queries != optimized.queries+1 || baseline.queryBytes <= optimized.queryBytes || baseline.rows != optimized.rows+1 || baseline.textBytes != optimized.textBytes || baseline.valueBytes != optimized.valueBytes+8 {
			t.Fatalf("SQL/returned-value work changed unexpectedly: baseline=%+v optimized=%+v", baseline, optimized)
		}
		if memberDeltaAudit(t, ctx, b) != before {
			t.Fatal("scalar optimization changed physical inventory")
		}
		t.Logf("actual driver query submissions=%d->%d submitted SQL text bytes=%d->%d rows=%d->%d returned string/blob bytes=%d->%d typed value lengths (8/int64)=%d->%d commits=1->1 external PrepareContext=0->0", baseline.queries, optimized.queries, baseline.queryBytes, optimized.queryBytes, baseline.rows, optimized.rows, baseline.textBytes, optimized.textBytes, baseline.valueBytes, optimized.valueBytes)
	})
	for _, name := range []string{"metadata_missing", "metadata_mismatch", "charged_field", "clear_field", "identity_change", "scope_edge", "scope_scalar", "second_mutation", "second_charged_mutation", "second_identity_mutation"} {
		t.Run(name, func(t *testing.T) {
			ctx, client, b, owned, row, probe, credit, _ := storageSQLFixture(t)
			if name == "clear_field" {
				if err := Transaction(owned, client, func(tx *ent.Client) error {
					return tx.WorkRequest.UpdateOneID(row.ID).SetCapturePayload([]byte("captured bytes")).SetCaptureBytes(14).Exec(owned)
				}); err != nil {
					t.Fatal(err)
				}
			}
			trusted := persistence.WithMetadata(owned, persistence.Metadata{RequestID: row.Identity})
			if name == "metadata_missing" {
				trusted = owned
			}
			if name == "metadata_mismatch" {
				trusted = persistence.WithMetadata(owned, persistence.Metadata{RequestID: "untrusted-request"})
			}
			physicalBefore := memberDeltaAudit(t, ctx, b)
			consumedBefore := credit.consumed
			probe.reset()
			err := Transaction(trusted, client, func(tx *ent.Client) error {
				u := tx.WorkRequest.UpdateOneID(row.ID).SetPromotionCursor(0)
				switch name {
				case "charged_field":
					u.SetResponseGlossary([]byte("charged glossary"))
				case "clear_field":
					u.ClearCapturePayload().SetCaptureBytes(0)
				case "identity_change":
					u.SetIdentity("changed-request")
				case "scope_edge":
					u.SetJobID(row.JobID)
				case "scope_scalar":
					u.SetRetryEpoch(row.RetryEpoch)
				}
				if err := u.Exec(trusted); err != nil {
					return err
				}
				if name == "second_mutation" {
					return tx.WorkRequest.UpdateOneID(row.ID).SetPromotionCursor(0).Exec(trusted)
				}
				if name == "second_charged_mutation" {
					return tx.WorkRequest.UpdateOneID(row.ID).SetResponseGlossary([]byte("charged glossary")).Exec(trusted)
				}
				if name == "second_identity_mutation" {
					return tx.WorkRequest.UpdateOneID(row.ID).SetIdentity("changed-request").Exec(trusted)
				}
				return nil
			})
			s := probe.stop()
			if err != nil {
				t.Fatal(err)
			}
			combined, separate := s.identityReads()
			wantCombined := 0
			if name == "metadata_mismatch" || strings.HasPrefix(name, "second_") {
				wantCombined = 1
			}
			if combined != wantCombined {
				t.Fatalf("unsafe mutation combined identity read: %d want=%d", combined, wantCombined)
			}
			if separate != 1 {
				t.Fatalf("required legacy identity/length read absent: %d", separate)
			}
			physicalAfter := memberDeltaAudit(t, ctx, b)
			if name == "charged_field" || name == "second_charged_mutation" {
				if credit.consumed-consumedBefore != int64(len("charged glossary")) || physicalAfter-physicalBefore != int64(len("charged glossary")) {
					t.Fatal("charged write bypassed reliable credit or physical bytes")
				}
			} else if name == "clear_field" {
				if physicalBefore-physicalAfter != int64(len("captured bytes")) {
					t.Fatal("clear failed to retire actual charged bytes")
				}
			} else if physicalAfter != physicalBefore {
				t.Fatal("scalar/identity update changed physical storage")
			}
		})
	}
}

func TestStorageBeforeIdentityUnknownACK(t *testing.T) {
	ctx, client, b, owned, row, probe, credit, faults := storageSQLFixture(t)
	before := memberDeltaAudit(t, ctx, b)
	trusted := persistence.WithMetadata(owned, persistence.Metadata{RequestID: row.Identity})
	faults.loseACK.Store(true)
	s, err := storageSQLUpdate(t, client, b, probe, credit, trusted, func(tx *ent.Client) error {
		return tx.WorkRequest.UpdateOneID(row.ID).SetPromotionCursor(0).Exec(trusted)
	})
	if !unknownCommit(err) {
		t.Fatalf("actual committed ACK loss was hidden: %v", err)
	}
	if combined, separate := s.identityReads(); combined != 1 || separate != 0 {
		t.Fatalf("unknown ACK projection=%d separate=%d", combined, separate)
	}
	if memberDeltaAudit(t, ctx, b) != before || credit.refunded != 0 {
		t.Fatal("unknown ACK guessed a refund or changed physical backing")
	}
	if err := b.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if memberDeltaAudit(t, ctx, b) != before {
		t.Fatal("duplicate settlement changed committed inventory")
	}
}

var _ driver.QueryerContext = (*storageSQLConn)(nil)
var _ driver.ExecerContext = (*storageSQLConn)(nil)
var _ driver.ConnBeginTx = (*storageSQLConn)(nil)
