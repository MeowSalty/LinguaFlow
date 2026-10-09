package progress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// segmentUpdate 记录一次 SegmentDone 事件的状态。
type segmentUpdate struct {
	done int64 // 当前轮次已完成的段落数（原子值快照）
}

// roundCheckpoint 是单个轮次行的断点缓冲：一个轮次行 + 该轮已解决段集合。
//
// resolved 只用于本 reporter 的展示与去重，pending 保存待确认的段 ID。
// aligned 表示本地视图已从数据库初始化；writtenCount 只抑制本地重复 flush，
// 不能代表数据库计数。实际断点基数始终由 workstate 在 Job 锁内维护，
// 不会被这里落后的内存集合覆盖。
//
// docIndex→Segment ID 的映射在 SegmentResolved 入缓冲时就完成（而非 flush
// 时才查 mapper），缓冲因此自包含——轮次切换后旧轮残留可独立重放，不依赖
// 「当轮 mapper 仍在位」。
type roundCheckpoint struct {
	rowID        int
	segmentID    func(docIndex int) (dbID int, ok bool)
	resolved     map[int]struct{}
	pending      []int
	aligned      bool // resolved 是否已与 DB 断点行对齐
	writtenCount int  // 本 reporter 上次成功 flush 的本地集合大小
}

// needsWrite 报告是否有新增事实或恢复后尚未确认的本地视图。
func (c *roundCheckpoint) needsWrite() bool {
	return len(c.pending) > 0 || len(c.resolved) != c.writtenCount
}

// snapshot 取出待确认的事实（调用方持 flushMu）。count 仅供成功后的本地
// 记账；数据库只接收 ids。空 ids 也可触发兼容轮次的断点缓存校准。
func (c *roundCheckpoint) snapshot() (checkpointWrite, bool) {
	if !c.aligned || !c.needsWrite() {
		return checkpointWrite{}, false
	}
	ids := c.pending
	c.pending = nil
	return checkpointWrite{round: c, ids: ids, count: len(c.resolved)}, true
}

// requeue 把写失败的增量放回队首（断点是正确性数据，必须重试）。
func (c *roundCheckpoint) requeue(ids []int) {
	c.pending = append(ids, c.pending...)
}

// checkpointWrite 是一次 flush 内针对单个轮次行的写入快照。
type checkpointWrite struct {
	round *roundCheckpoint
	ids   []int // 本次插入的 Segment ID
	count int   // flush 成功后的本地视图备忘，不写入数据库
}

// DBReporter 将执行进度写入数据库，实现 Reporter 接口。
// 采用双触发条件的缓冲区策略：BatchComplete() 立即 flush + 定时器安全网。
//
// job_round_segments 是完成事实；segment_completed 与 Job 进度都是派生缓存。
// 本 reporter 与候选提交共用 workstate.Confirm，在同一 Job 事务内去重、
// 维护轮次基数并按终态有效口径推进 Job。源删除的级联清理也在该边界校准。
// 无轮次行时保留单资源兼容语义，SegmentResolved 为 no-op。
//
// 并发约定：flushMu 保护「计数缓冲 + 当前轮次记录 + 旧轮残留队列」的快照
// 一致性（ticker goroutine 与调用方 goroutine 交叉 flush 时不出现「旧缓冲
// 写新行」）；mu 保护 stageName/stageTotal 展示态；stageDone 为原子计数。
type DBReporter struct {
	client        *ent.Client
	jobID         int
	jobResourceID int
	logger        *slog.Logger
	broker        *event.Broker

	// round 是当前活跃轮次的断点缓冲；nil 表示无轮次行（单资源路径）。
	// stale 是切轮时仍有未落库断点的旧轮记录：断点是正确性数据，不能随
	// 切轮丢弃，后续 flush 按各自的行 ID 继续重试。均受 flushMu 保护。
	round *roundCheckpoint
	stale []*roundCheckpoint

	// pending 是 SegmentDone 计数缓冲，仅无轮次行的退化路径用它推进
	// Job.progress_completed（有轮次行时增量由断点集合基数派生）。
	// 受 flushMu 保护。
	pending []segmentUpdate

	// flushMu 保护快照字段；flushWriteMu 让快照到写回/重排队成为一个不可交叉的
	// flush 生命周期，避免切轮恰好落在失败写回之前而遗漏 stale 记录。
	flushMu      sync.Mutex
	flushWriteMu sync.Mutex

	// 轮次内段完成计数
	stageDone atomic.Int64

	// 轮次展示态
	mu            sync.Mutex
	stageName     string
	stageTotal    int
	stageErr      error
	checkpointErr error

	// 定时器安全网
	ticker       *time.Ticker
	done         chan struct{}
	once         sync.Once
	tickerExited chan struct{}
	sourceMu     sync.RWMutex
	closed       bool
	closeErr     error

	// flush 函数，方便测试注入
	flushFn func([]segmentUpdate) error
}

// DBReporterOptions 是 DBReporter 的配置选项。
// 轮次目标行与断点映射经 SwitchRound 运行时注入
// （每轮切换各注入一次，单一注入通道）。
type DBReporterOptions struct {
	Client        *ent.Client
	JobID         int
	JobResourceID int
	Logger        *slog.Logger
	Ticker        time.Duration // flush 安全网间隔，默认 2s
	Broker        *event.Broker // 事件 Broker，nil 时跳过事件推送
}

// NewDBReporter 创建一个新的 DBReporter 实例。
// 调用方需确保 Close() 被调用以释放资源。
func NewDBReporter(opts DBReporterOptions) *DBReporter {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	tickerDur := opts.Ticker
	if tickerDur <= 0 {
		tickerDur = 2 * time.Second
	}

	r := &DBReporter{
		client:        opts.Client,
		jobID:         opts.JobID,
		jobResourceID: opts.JobResourceID,
		logger:        logger,
		broker:        opts.Broker,
		ticker:        time.NewTicker(tickerDur),
		done:          make(chan struct{}),
		tickerExited:  make(chan struct{}),
	}

	go r.runTicker()

	return r
}

// SwitchRound 切换当前轮次行，并注入该轮的 docIndex→DB Segment ID 映射
// （轮次边界时由 runner 调用，每轮恰好一次——轮次行与其断点映射是同一件事，
// 单点注入使二者不可能错配）。
//
// 顺序语义：先 flush 上一轮残余缓冲（写入旧行），再切换目标行并重置段计数。
// 旧轮若仍有待写内容（未落库的断点增量，或尚未申明的计数值——needsWrite），
// 整条记录移入 stale 队列由后续 flush 按旧行 ID 重试——断点是正确性数据，
// 随切轮丢弃会让已产出业务结果的段在恢复后被重扫、重复调用 LLM。
//
// roundRowID <= 0 退化为「无轮次行」：mapper 忽略、SegmentResolved 为 no-op，
// 进度只累加 Job 计数器（单资源路径）。
func (r *DBReporter) SwitchRound(roundRowID int, segmentID func(docIndex int) (dbID int, ok bool)) {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	r.flushWriteMu.Lock()
	// flushLocked 与切轮共享同一写入生命周期锁：失败重排队完成前不能切走
	// 当前轮，否则刚放回的 pending 无法被移入 stale。
	r.flushLocked()

	if roundRowID > 0 && segmentID == nil {
		// 有轮次行必须有映射：否则本轮无法登记任何断点，segment_completed
		// 恒为 0（进度停滞）。这是接线错误，显式点名而不静默退化。
		r.logger.Error("DBReporter: round row without segment mapper, round progress will not advance",
			"job_id", r.jobID, "round_row_id", roundRowID)
	}

	r.flushMu.Lock()
	if prev := r.round; prev != nil && prev.needsWrite() {
		r.stale = append(r.stale, prev)
	}
	if roundRowID > 0 {
		r.round = &roundCheckpoint{
			rowID:     roundRowID,
			segmentID: segmentID,
			resolved:  make(map[int]struct{}),
		}
	} else {
		r.round = nil
	}
	r.flushMu.Unlock()
	r.stageDone.Store(0)
	r.flushWriteMu.Unlock()
}

// SegmentResolved 登记一个已解决段（executor 对已解决子集逐段调用，与
// SegmentDone 配对）。实现 progress.SegmentResolvedNotifier。
//
// 入缓冲即完成 docIndex→DB Segment ID 映射：无轮次行/无映射时 no-op；
// mapper 拒绝（ok=false）的段跳过登记；已在集合内的段跳过（幂等，重扫/
// 重放安全）。只入缓冲不触发 flush——flush 由调用方的 BatchComplete 与
// ticker 驱动，与 segment_completed 在同一事务推进。
func (r *DBReporter) SegmentResolved(docIndex int) {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	r.flushMu.Lock()
	defer r.flushMu.Unlock()

	c := r.round
	if c == nil || c.segmentID == nil {
		return
	}
	dbID, ok := c.segmentID(docIndex)
	if !ok {
		return
	}
	if _, seen := c.resolved[dbID]; seen {
		return
	}
	c.resolved[dbID] = struct{}{}
	c.pending = append(c.pending, dbID)
}

// StageStart 在事务内初始化兼容轮次分母并校准断点缓存，已封口清单保持原总量。
// 本地展示基线另从完成事实恢复。任一步失败都保存为 StageError，执行器必须
// 在派发前检查，不能降级为部分写入后继续执行。
func (r *DBReporter) StageStart(name string, total int) {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	r.flushWriteMu.Lock()
	defer r.flushWriteMu.Unlock()
	r.mu.Lock()
	r.stageName, r.stageTotal, r.stageErr = name, total, nil
	r.mu.Unlock()
	r.flushMu.Lock()
	r.pending = r.pending[:0]
	round := r.round
	r.flushMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := workstate.Transaction(ctx, r.client, func(tx *ent.Client) error {
		if err := workstate.LockJob(ctx, tx, r.jobID); err != nil {
			return err
		}
		if round == nil {
			return tx.Job.UpdateOneID(r.jobID).AddProgressTotal(int64(total)).Exec(ctx)
		}
		if err := workstate.InitializeTotal(ctx, tx, r.jobID, round.rowID, total); err != nil {
			return err
		}
		return workstate.Calibrate(ctx, tx, r.jobID)
	})
	r.mu.Lock()
	r.stageErr = err
	r.mu.Unlock()
	if err != nil {
		r.logger.Error("round initialization failed", "job_id", r.jobID, "error", err)
		return
	}
	baseline := 0
	if round != nil {
		if n, err := r.alignResolved(ctx, round); err == nil {
			baseline = n
		} else {
			r.mu.Lock()
			r.stageErr = fmt.Errorf("restore round %d checkpoint baseline: %w", round.rowID, err)
			r.mu.Unlock()
			return
		}
	}
	r.stageDone.Store(int64(baseline))
	r.publishEvent("stage_start", name, fmt.Sprintf("轮次开始: %s (%d 段)", name, total))
}

// StageError exposes failed durable initialization to the executor before it
// admits requests. StageStart itself remains compatible with display reporters.
func (r *DBReporter) StageError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(r.stageErr, r.checkpointErr)
}

// alignResolved 以 DB 断点行为准对齐该轮内存集合，返回对齐后的集合基数与成败。
// 对齐 = 把 resolved 重建为「已落库 ∪ pending 残余」而非往旧集合里并：既不在
// DB 也不在 pending 的成员没有任何落库路径，保留只会虚增基数；pending 中已
// 落库的 ID（上一轮运行已写入的恢复重扫段）剔除。查询不持 flushMu（DB IO 不
// 阻塞 SegmentResolved 登记）；查询与加锁之间到达的登记落进 pending，重建时
// 一并拾取，不丢。对齐成功后该轮才具备写 segment_completed 的资格（needsWrite
// 的计数分支自此可信）。
//
// 失败会返回给执行器，本轮必须先重试存储屏障才能继续派发。
func (r *DBReporter) alignResolved(ctx context.Context, c *roundCheckpoint) (int, error) {
	ids, err := r.client.JobRoundSegment.Query().
		Where(jobroundsegment.JobRoundIDEQ(c.rowID)).
		Select(jobroundsegment.FieldSegmentID).
		Ints(ctx)
	if err != nil {
		r.logger.Warn("DBReporter: failed to load round checkpoints, round progress write deferred until aligned",
			"job_id", r.jobID, "round_row_id", c.rowID, "error", err)
		return 0, err
	}
	persisted := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		persisted[id] = struct{}{}
	}
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	kept := c.pending[:0]
	for _, id := range c.pending {
		if _, dup := persisted[id]; !dup {
			kept = append(kept, id)
		}
	}
	c.pending = kept
	resolved := make(map[int]struct{}, len(persisted)+len(kept))
	for id := range persisted {
		resolved[id] = struct{}{}
	}
	for _, id := range kept {
		resolved[id] = struct{}{}
	}
	c.resolved = resolved
	c.aligned = true
	return len(c.resolved), nil
}

// alignCheckpoints 把所有未对齐的轮次断点集合与 DB 对齐（flush 写
// segment_completed 的前置条件）：从 stale 与当前轮收集未对齐者逐个重建。
// 无轮次行（round 与 stale 皆空）时自然为 no-op——flushFn 测试注入路径因此
// 保持无 DB。单个对齐失败不阻断其余轮次：失败者保持未对齐、不产出写入，
// 留待下次 flush 重试。
func (r *DBReporter) alignCheckpoints(ctx context.Context) error {
	r.flushMu.Lock()
	var unaligned []*roundCheckpoint
	for _, c := range r.stale {
		if !c.aligned {
			unaligned = append(unaligned, c)
		}
	}
	if c := r.round; c != nil && !c.aligned {
		unaligned = append(unaligned, c)
	}
	r.flushMu.Unlock()
	if len(unaligned) == 0 {
		return nil
	}
	var result error
	for _, c := range unaligned {
		if _, err := r.alignResolved(ctx, c); err != nil {
			result = errors.Join(result, fmt.Errorf("restore round %d checkpoint baseline: %w", c.rowID, err))
		}
	}
	return result
}

// SegmentDone 记录一个段落完成：推进轮次展示计数，并追加计数缓冲。
// 计数缓冲只在无轮次行的退化路径参与 Job.progress_completed 累加——
// 有轮次行时进度增量由断点集合基数派生（见 flush）。
func (r *DBReporter) SegmentDone() {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	cur := r.stageDone.Add(1)

	r.flushMu.Lock()
	r.pending = append(r.pending, segmentUpdate{done: cur})
	r.flushMu.Unlock()
}

// BatchComplete 批次完成时调用，立即触发缓冲区 flush。
func (r *DBReporter) BatchComplete() {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	r.flush()
}

// FlushCheckpoint is the durable barrier before a legacy executor releases a
// batch or starts another model request. Storage retries reuse the same facts.
func (r *DBReporter) FlushCheckpoint(ctx context.Context) error {
	if !r.beginWrite() {
		return fmt.Errorf("checkpoint reporter is closed")
	}
	defer r.sourceMu.RUnlock()
	r.flushWriteMu.Lock()
	defer r.flushWriteMu.Unlock()
	return r.flushLockedContext(ctx)
}

// StageDone 记录当前轮次完成。
func (r *DBReporter) StageDone() {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	// 轮次结束时做一次最终 flush，确保所有进度写入 DB
	if err := r.flush(); err != nil {
		return
	}

	r.mu.Lock()
	stageName := r.stageName
	done := r.stageDone.Load()
	r.mu.Unlock()

	// Publish stage_done event
	r.publishEvent("stage_done", stageName, fmt.Sprintf("轮次完成: %s (%d 段)", stageName, done))
}

// beginWrite admits a producer until Close starts. The read lock covers the
// complete call, including synchronous persistence and event publication.
func (r *DBReporter) beginWrite() bool {
	r.sourceMu.RLock()
	if r.closed {
		r.sourceMu.RUnlock()
		return false
	}
	return true
}

// Close joins producers and the ticker before the final flush. Once it returns,
// callers may release execution ownership even when the final flush failed.
func (r *DBReporter) Close() error {
	r.once.Do(func() {
		r.sourceMu.Lock()
		r.closed = true
		close(r.done)
		r.ticker.Stop()
		r.sourceMu.Unlock()
		if r.tickerExited != nil {
			<-r.tickerExited
		}
		// 最后一次 flush
		r.closeErr = r.flush()
		r.reportCheckpointResidue()

		// Publish final event
		if r.closeErr == nil && r.StageError() == nil {
			r.publishEvent("stage_done", "", "资源处理完成")
		}
	})
	return r.closeErr
}

// reportCheckpointResidue 在关闭时点名两类未落库残留（DB 持续故障时可能发生），
// 都是需要人工关注的数据面偏差，用 ERROR 级点名到具体轮次行：
//   - 断点增量残留（pending 非空）：这些段的业务结果已产出但断点缺失，恢复后
//     会被重扫并重复调用 LLM；
//   - 纯计数残留（pending 已空但计数未申明）：断点行齐全，只是 segment_completed
//     没写成集合基数——对齐始终失败，或对齐后那次纯计数写入失败。此时该轮读侧
//     进度低于真实断点行数，且不会自愈到下次重跑对齐为止（本 reporter 已关闭）。
//     前一类同时隐含计数落后，重扫本身就会把它带上来，故不重复点名。
func (r *DBReporter) reportCheckpointResidue() {
	type residue struct {
		rowID    int
		segments int
	}
	var left []residue
	collect := func(c *roundCheckpoint) {
		if c == nil || !c.needsWrite() {
			return
		}
		left = append(left, residue{rowID: c.rowID, segments: len(c.pending)})
	}
	r.flushMu.Lock()
	for _, c := range r.stale {
		collect(c)
	}
	collect(r.round)
	r.flushMu.Unlock()

	for _, res := range left {
		if res.segments > 0 {
			r.logger.Error("DBReporter: round checkpoints unflushed at close, resumed run will rescan these segments",
				"job_id", r.jobID,
				"job_resource_id", r.jobResourceID,
				"round_row_id", res.rowID,
				"segments", res.segments)
			continue
		}
		r.logger.Error("DBReporter: round segment_completed left unasserted at close, round progress reads below its checkpoint rows until a rerun realigns it",
			"job_id", r.jobID,
			"job_resource_id", r.jobResourceID,
			"round_row_id", res.rowID)
	}
}

// runTicker 后台定时器协程，按间隔调用 flush()。
func (r *DBReporter) runTicker() {
	if r.tickerExited != nil {
		defer close(r.tickerExited)
	}
	for {
		select {
		case <-r.done:
			return
		case <-r.ticker.C:
			r.flush()
		}
	}
}

// flush 取出所有待处理写入（各轮断点增量 + 退化路径的计数缓冲）并执行。
// 同一临界区快照「计数缓冲 + 各轮断点增量」，保证并发 flush（ticker 与调用方
// goroutine 交叉）时不会出现「旧缓冲写新行」的错位；写入前先把未对齐的轮次
// 集合与 DB 对齐（写 segment_completed 的前置条件，见 alignCheckpoints），
// 写回由 writeFlush 在单个事务内完成（checkpoint 不变式：segment_completed
// 与断点集合基数同步推进）。
func (r *DBReporter) flush() error {
	r.flushWriteMu.Lock()
	defer r.flushWriteMu.Unlock()
	return r.flushLocked()
}

func (r *DBReporter) flushLocked() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.flushLockedContext(ctx)
}

func (r *DBReporter) flushLockedContext(ctx context.Context) (err error) {
	defer func() { r.mu.Lock(); r.checkpointErr = err; r.mu.Unlock() }()
	// 对齐是写 segment_completed 的前置条件：未对齐的轮次先以 DB 为准重建
	// 集合。无轮次行时该步自然为 no-op，flushFn 测试注入路径因此保持无 DB。
	if err := r.alignCheckpoints(ctx); err != nil {
		return err
	}

	r.flushMu.Lock()
	updates := r.pending
	r.pending = make([]segmentUpdate, 0, len(updates))
	if r.flushFn != nil {
		// 测试注入路径：flushFn 只吞 updates，断点增量不透传
		//（断点行为用真实 client 的测试覆盖）。
		r.flushMu.Unlock()
		if len(updates) == 0 {
			return nil
		}
		return r.flushFn(updates)
	}
	// 旧轮残留先写：各自按记录里的行 ID 落库，与当前轮互不干扰。
	writes := make([]checkpointWrite, 0, len(r.stale)+1)
	for _, c := range r.stale {
		if w, ok := c.snapshot(); ok {
			writes = append(writes, w)
		}
	}
	if c := r.round; c != nil {
		if w, ok := c.snapshot(); ok {
			writes = append(writes, w)
		}
	}
	// 计数缓冲只在无轮次行时推进 Job 计数器：有轮次行时增量恒由断点集合
	// 基数派生，两者相加会双计。
	jobDelta := 0
	if r.round == nil {
		jobDelta = len(updates)
	}

	if len(writes) == 0 && jobDelta == 0 {
		r.flushMu.Unlock()
		return nil
	}

	// 数据库只接收快照中的新增 ID；其后的事件留在 pending 中。释放 flushMu
	// 避免事件收集等待数据库，flushWriteMu 仍串行化完整 flush 生命周期。
	r.flushMu.Unlock()

	err = r.writeFlush(ctx, writes, jobDelta)
	if len(writes) > 0 {
		// 写回记账同一临界区完成：成功记下已提交的计数值（写抑制备忘，此后
		// 集合不再增长就无需重写）；失败则断点是正确性数据，放回各记录队首
		// 下次重试（计数缓冲丢失是既有行为——它只是 Job 计数器的展示增量）。
		r.flushMu.Lock()
		for _, w := range writes {
			if err != nil {
				w.round.requeue(w.ids)
			} else {
				w.round.writtenCount = w.count
			}
		}
		r.flushMu.Unlock()
	}
	if err != nil && ent.IsConstraintError(err) {
		// 约束错误后重新加载本地视图；重复 ID 已由共享 writer 幂等处理，
		// 此处分支用于真实约束异常，不依赖唯一冲突承担正常去重。
		r.flushMu.Lock()
		for _, w := range writes {
			w.round.aligned = false
		}
		r.flushMu.Unlock()
	}
	r.pruneStale()
	return err
}

// pruneStale 把不再需要写入的旧轮记录出队（无待插增量且计数已申明），避免
// stale 随轮次数无界增长。
func (r *DBReporter) pruneStale() {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	kept := r.stale[:0]
	for _, c := range r.stale {
		if c.needsWrite() {
			kept = append(kept, c)
		}
	}
	r.stale = kept
}

// writeFlush 只提交完成事实，共享 writer 在 Job 锁内去重并维护全部派生进度。
// 不向数据库回写 checkpointWrite.count，因其他消费者可能已经确认更多事实。
func (r *DBReporter) writeFlush(ctx context.Context, writes []checkpointWrite, jobDelta int) error {
	return workstate.Transaction(ctx, r.client, func(tx *ent.Client) error {
		if err := workstate.LockJob(ctx, tx, r.jobID); err != nil {
			return err
		}
		for _, w := range writes {
			facts := make([]workstate.Confirmation, 0, len(w.ids))
			for _, id := range w.ids {
				facts = append(facts, workstate.Confirmation{SegmentID: id})
			}
			if _, err := workstate.Confirm(ctx, tx, r.jobID, w.round.rowID, facts); err != nil {
				return err
			}
		}
		if jobDelta > 0 {
			return tx.Job.UpdateOneID(r.jobID).AddProgressCompleted(int64(jobDelta)).Exec(ctx)
		}
		return nil
	})
}

// publishEvent publishes a lifecycle event to the Broker. No-op if broker is nil.
func (r *DBReporter) publishEvent(eventType, stage, message string) {
	if r.broker == nil {
		return
	}
	r.broker.Publish(r.jobID, event.Event{
		Type:      eventType,
		JobID:     r.jobID,
		Level:     "info",
		Stage:     stage,
		Message:   message,
		CreatedAt: timeutil.NowUTC(),
	})
}

// OnBatchEvent implements BatchObserver. Publishes batch events to the Broker.
func (r *DBReporter) OnBatchEvent(batchEvent BatchEvent) {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	if r.broker == nil {
		return
	}
	sent, sentTrunc, sentLen := TruncateSSEContent(batchEvent.SentContent)
	recv, recvTrunc, recvLen := TruncateSSEContent(batchEvent.ReceivedContent)
	metadata := map[string]any{
		"segment_ids":      batchEvent.SegmentIDs,
		"segment_count":    batchEvent.SegmentCount,
		"backend_name":     batchEvent.BackendName,
		"status":           batchEvent.Status,
		"duration_ms":      batchEvent.DurationMs,
		"input_tokens":     batchEvent.InputTokens,
		"output_tokens":    batchEvent.OutputTokens,
		"sent_content":     sent,
		"received_content": recv,
		"tried_backends":   batchEvent.TriedBackends,
		"shrink_attempted": batchEvent.ShrinkAttempted,
		"truncated":        batchEvent.Truncated,
		"sent_length":      sentLen,
		"received_length":  recvLen,
	}
	if sentTrunc {
		metadata["sent_truncated"] = true
	}
	if recvTrunc {
		metadata["received_truncated"] = true
	}
	if len(batchEvent.UsedGlossary) > 0 {
		metadata["used_glossary"] = batchEvent.UsedGlossary
	}
	if len(batchEvent.AddedGlossary) > 0 {
		metadata["added_glossary"] = batchEvent.AddedGlossary
	}
	if len(batchEvent.Repaired) > 0 {
		metadata["repaired"] = batchEvent.Repaired
	}
	if batchEvent.ErrorType != "" {
		metadata["error_type"] = batchEvent.ErrorType
	}
	if batchEvent.ErrorMessage != "" {
		metadata["error_message"] = batchEvent.ErrorMessage
	}
	if batchEvent.HTTPStatus > 0 {
		metadata["http_status"] = batchEvent.HTTPStatus
	}
	if batchEvent.RoundIndex > 0 {
		metadata["round_index"] = batchEvent.RoundIndex
	}
	if batchEvent.Attempt > 0 {
		metadata["attempt"] = batchEvent.Attempt
	}
	if batchEvent.ResponseFormat != "" {
		metadata["response_format"] = batchEvent.ResponseFormat
	}
	if len(batchEvent.JSONSchema) > 0 {
		metadata["json_schema"] = batchEvent.JSONSchema
	}
	if batchEvent.ParentRequestID != "" {
		metadata["parent_request_id"] = batchEvent.ParentRequestID
	}
	if batchEvent.CandidateID != "" {
		metadata["candidate_id"] = batchEvent.CandidateID
		metadata["candidate_version"] = batchEvent.CandidateVersion
	}
	if batchEvent.Stage == "ruby_alignment" {
		metadata["logical_attempt"] = batchEvent.LogicalAttempt
		metadata["network_attempt"] = batchEvent.NetworkAttempt
		metadata["verified_items"] = batchEvent.VerifiedItems
		metadata["missing_items"] = batchEvent.MissingItems
	}
	r.broker.Publish(r.jobID, event.Event{
		Type:      "batch",
		JobID:     r.jobID,
		Level:     BatchLevelFromStatus(batchEvent.Status),
		Stage:     batchEvent.Stage,
		Message:   fmt.Sprintf("batch (%d segs): %s", batchEvent.SegmentCount, batchEvent.Status),
		Metadata:  metadata,
		CreatedAt: timeutil.NowUTC(),
	})
}

// OnPoolEvent implements PoolObserver. Publishes pool-level events to the Broker.
// pool_advance 用 warn 级（仍有未解决段），pool_start 用 info 级。
// shrink=1.0（不缩）时用"重切"措辞；shrink<1.0（缩比）时用"缩批/缩放"措辞。
func (r *DBReporter) OnPoolEvent(poolEvent PoolEvent) {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	if r.broker == nil {
		return
	}
	level := "info"
	var message string
	if poolEvent.ShrinkRate >= 1.0 {
		// 不缩：多池同尺寸重切
		message = fmt.Sprintf("%s 池 %d/%d 开始：%d 批，%d 段",
			poolEvent.Mode, poolEvent.PoolIndex+1, poolEvent.MaxPools,
			poolEvent.Batches, poolEvent.Pending)
		if poolEvent.Phase == "pool_advance" {
			level = "warn"
			message = fmt.Sprintf("%s 重切：池 %d 未能全部解决，%d 段进入池 %d/%d",
				poolEvent.Mode, poolEvent.PoolIndex+1,
				poolEvent.Pending, poolEvent.PoolIndex+2, poolEvent.MaxPools)
		}
	} else {
		// 现有"缩批/缩放"措辞
		message = fmt.Sprintf("%s 池 %d/%d 开始：%d 批，%d 段（缩放 %.2f）",
			poolEvent.Mode, poolEvent.PoolIndex+1, poolEvent.MaxPools,
			poolEvent.Batches, poolEvent.Pending, poolEvent.ShrinkRate)
		if poolEvent.Phase == "pool_advance" {
			level = "warn"
			message = fmt.Sprintf("%s 缩批：池 %d 未能全部解决，%d 段进入池 %d/%d（缩放 %.2f）",
				poolEvent.Mode, poolEvent.PoolIndex+1,
				poolEvent.Pending, poolEvent.PoolIndex+2, poolEvent.MaxPools, poolEvent.ShrinkRate)
		}
	}

	metadata := map[string]any{
		"mode":        poolEvent.Mode,
		"pool_index":  poolEvent.PoolIndex,
		"max_pools":   poolEvent.MaxPools,
		"batches":     poolEvent.Batches,
		"pending":     poolEvent.Pending,
		"shrink_rate": poolEvent.ShrinkRate,
		"phase":       poolEvent.Phase,
	}

	r.broker.Publish(r.jobID, event.Event{
		Type:      "pool",
		JobID:     r.jobID,
		Level:     level,
		Stage:     poolEvent.Mode,
		Message:   message,
		Metadata:  metadata,
		CreatedAt: timeutil.NowUTC(),
	})
}
