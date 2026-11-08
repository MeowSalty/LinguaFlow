package persistenceacceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect"
)

var ErrInjected = errors.New("acceptance injected SQL failure")

type Sample struct {
	Name      string         `json:"name"`
	Driver    string         `json:"driver"`
	SQL       int            `json:"sql"`
	Begins    int            `json:"begins"`
	Commits   int            `json:"commits"`
	Rollbacks int            `json:"rollbacks"`
	ElapsedMS float64        `json:"elapsed_ms"`
	SQLP50MS  float64        `json:"sql_p50_ms"`
	SQLP95MS  float64        `json:"sql_p95_ms"`
	TxnP50MS  float64        `json:"txn_p50_ms"`
	TxnP95MS  float64        `json:"txn_p95_ms"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// Probe counts actual dialect Query/Exec calls (including calls inside Tx),
// separately from transaction boundaries. Delay is per statement, cancelable.
type Probe struct {
	mu                              sync.Mutex
	delay                           time.Duration
	sql, begins, commits, rollbacks int
	durations                       []time.Duration
	txnDurations                    []time.Duration
	started                         time.Time
	failAt                          int
	loseCommit                      bool
	loseBeforeCommit                bool
	unavailableAfterCommit          bool
	verificationUnavailable         bool
	cancelAfterCommit               context.CancelFunc
	failStatement                   string
	failOccurrence, matched         int
}

func (p *Probe) Reset(delay time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delay = delay
	p.sql = 0
	p.begins = 0
	p.commits = 0
	p.rollbacks = 0
	p.durations = nil
	p.txnDurations = nil
	p.started = time.Now()
	p.failAt = 0
	p.loseCommit = false
	p.loseBeforeCommit = false
	p.unavailableAfterCommit = false
	p.verificationUnavailable = false
	p.cancelAfterCommit = nil
	p.failStatement = ""
	p.matched = 0
}
func (p *Probe) FailAt(n int)      { p.mu.Lock(); p.failAt = n; p.mu.Unlock() }
func (p *Probe) LoseNextCommit()   { p.mu.Lock(); p.loseCommit = true; p.mu.Unlock() }
func (p *Probe) LoseBeforeCommit() { p.mu.Lock(); p.loseBeforeCommit = true; p.mu.Unlock() }
func (p *Probe) LoseCommitAndCancel(cancel context.CancelFunc) {
	p.mu.Lock()
	p.loseCommit = true
	p.cancelAfterCommit = cancel
	p.mu.Unlock()
}
func (p *Probe) LoseCommitWithUnavailableVerification() {
	p.mu.Lock()
	p.loseCommit = true
	p.unavailableAfterCommit = true
	p.mu.Unlock()
}
func (p *Probe) RestoreVerification() { p.mu.Lock(); p.verificationUnavailable = false; p.mu.Unlock() }
func (p *Probe) FailStatement(contains string, occurrence int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failStatement = contains
	p.failOccurrence = occurrence
	p.matched = 0
}
func (p *Probe) SQLCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.sql }
func (p *Probe) call(ctx context.Context, query string, fn func() error) error {
	start := time.Now()
	p.mu.Lock()
	p.sql++
	n, delay, fail := p.sql, p.delay, p.failAt
	matchedFailure := false
	if p.failStatement != "" && strings.Contains(strings.ReplaceAll(query, "`", "\""), p.failStatement) {
		p.matched++
		matchedFailure = p.matched == p.failOccurrence
	}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.durations = append(p.durations, time.Since(start)); p.mu.Unlock() }()
	if matchedFailure || fail > 0 && n == fail {
		return ErrInjected
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
func (p *Probe) Snapshot(name string, extra map[string]any) Sample {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := append([]time.Duration(nil), p.durations...)
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	q := func(f float64) float64 {
		if len(d) == 0 {
			return 0
		}
		return float64(d[min(len(d)-1, int(float64(len(d)-1)*f))]) / float64(time.Millisecond)
	}
	tx := append([]time.Duration(nil), p.txnDurations...)
	sort.Slice(tx, func(i, j int) bool { return tx[i] < tx[j] })
	tq := func(f float64) float64 {
		if len(tx) == 0 {
			return 0
		}
		return float64(tx[min(len(tx)-1, int(float64(len(tx)-1)*f))]) / float64(time.Millisecond)
	}
	return Sample{Name: name, Driver: Driver(), SQL: p.sql, Begins: p.begins, Commits: p.commits, Rollbacks: p.rollbacks, ElapsedMS: float64(time.Since(p.started)) / float64(time.Millisecond), SQLP50MS: q(.5), SQLP95MS: q(.95), TxnP50MS: tq(.5), TxnP95MS: tq(.95), Extra: extra}
}

var reportMu sync.Mutex

func WriteSample(s Sample) error {
	path := os.Getenv("LINGUAFLOW_PERSISTENCE_REPORT")
	if path == "" {
		return nil
	}
	reportMu.Lock()
	defer reportMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(s)
}

type observedDriver struct {
	dialect.Driver
	probe *Probe
}

func (d *observedDriver) Exec(ctx context.Context, q string, a, v any) error {
	return d.probe.call(ctx, q, func() error { return d.Driver.Exec(ctx, q, a, v) })
}
func (d *observedDriver) Query(ctx context.Context, q string, a, v any) error {
	return d.probe.call(ctx, q, func() error { return d.Driver.Query(ctx, q, a, v) })
}
func (d *observedDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	d.probe.mu.Lock()
	if d.probe.verificationUnavailable {
		d.probe.mu.Unlock()
		return nil, errors.New("acceptance verification connection unavailable")
	}
	d.probe.begins++
	d.probe.mu.Unlock()
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &observedTx{Tx: tx, probe: d.probe, started: time.Now()}, nil
}
func (d *observedDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	d.probe.mu.Lock()
	unavailable := d.probe.verificationUnavailable
	d.probe.mu.Unlock()
	if unavailable {
		return nil, errors.New("acceptance verification connection unavailable")
	}
	if driver, ok := d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	}); ok {
		d.probe.mu.Lock()
		d.probe.begins++
		d.probe.mu.Unlock()
		tx, err := driver.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &observedTx{Tx: tx, probe: d.probe, started: time.Now()}, nil
	}
	return d.Tx(ctx)
}

type observedTx struct {
	dialect.Tx
	probe    *Probe
	started  time.Time
	finished sync.Once
}

func (t *observedTx) finish() {
	t.finished.Do(func() {
		t.probe.mu.Lock()
		t.probe.txnDurations = append(t.probe.txnDurations, time.Since(t.started))
		t.probe.mu.Unlock()
	})
}

func (t *observedTx) Exec(ctx context.Context, q string, a, v any) error {
	return t.probe.call(ctx, q, func() error { return t.Tx.Exec(ctx, q, a, v) })
}
func (t *observedTx) Query(ctx context.Context, q string, a, v any) error {
	return t.probe.call(ctx, q, func() error { return t.Tx.Query(ctx, q, a, v) })
}
func (t *observedTx) Commit() error {
	defer t.finish()
	t.probe.mu.Lock()
	before := t.probe.loseBeforeCommit
	t.probe.loseBeforeCommit = false
	t.probe.mu.Unlock()
	if before {
		return errors.New("acceptance connection lost before COMMIT delivery")
	}
	err := t.Tx.Commit()
	t.probe.mu.Lock()
	t.probe.commits++
	lose := t.probe.loseCommit
	if err == nil && lose {
		t.probe.loseCommit = false
		if t.probe.unavailableAfterCommit {
			t.probe.verificationUnavailable = true
		}
	}
	cancel := t.probe.cancelAfterCommit
	t.probe.mu.Unlock()
	if err == nil && lose {
		if cancel != nil {
			cancel()
		}
		return errors.New("acceptance connection lost after COMMIT")
	}
	return err
}
func (t *observedTx) Rollback() error {
	defer t.finish()
	err := t.Tx.Rollback()
	t.probe.mu.Lock()
	t.probe.rollbacks++
	t.probe.mu.Unlock()
	return err
}
