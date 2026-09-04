package progress

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

// segmentUpdate 记录一次 SegmentDone 事件的状态。
type segmentUpdate struct {
	done int64 // 当前轮次已完成的段落数（原子值快照）
}

// roundCheckpoint 是单个轮次行的断点缓冲：一个轮次行 + 该轮已解决段集合。
//
// resolved 是「本轮已解决段」的 DB Segment ID 集合（已落库 ∪ 待落库），
// 也是该轮 segment_completed 的唯一事实源；pending 是尚未落库的增量。
// 「resolved 与 DB 断点行一致」不是默认成立的推断而是显式状态：
//   - aligned：集合是否已与 DB 对齐。SegmentResolved 只做增量登记，未对齐
//     时 len(resolved) 不代表 DB 真相，不得作为 segment_completed 写入；
//     对齐由 alignResolved 以 DB 断点行重建集合完成（StageStart 与每次
//     flush 写入前）。
//   - writtenCount：本 reporter 上次成功提交到 segment_completed 列的值，
//     纯写抑制备忘，不是列的镜像（从不回读该列——回读会把非断点来源的值引进
//     判据，正是这套设计要避免的）。初值 0 匹配新建的空集合，全新空轮因此不
//     产生多余写入；恢复轮对齐出非空集合后 len(resolved) != 0，首次 flush 会
//     发一条无插入的纯计数申明把列写成集合基数——这正是恢复时纠偏所需，代价
//     是每个恢复轮多一次幂等的绝对值写。
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
	writtenCount int  // 本 reporter 上次成功写入 segment_completed 的值
}

// needsWrite 报告本轮是否还有待落库的写入：有待插入的断点增量，或计数列与
// 集合基数不一致（对齐后重新断言 / 写失败后重试）。
func (c *roundCheckpoint) needsWrite() bool {
	return len(c.pending) > 0 || len(c.resolved) != c.writtenCount
}

// snapshot 取出一次完整的断点写入条目（调用方持 flushMu）。前提是对齐：
// 对齐后待写增量恒 ⊆ resolved，且 resolved 的其余成员恒已落库，插入 pending
// 后 DB 集合恰为 resolved，len(resolved) 即 segment_completed——未对齐的轮次
// 一律不产出写入（此时 len(resolved) 不是 DB 真相，写出去只会倒退），返回
// ok=false。ids 可为空：条目可以只为重申计数列而存在（对齐后的首次申明、
// 写失败后的重试），此时 needsWrite 仅由计数分支驱动。
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
	count int   // 插入后该轮断点集合基数（写入 segment_completed）
}

// DBReporter 将执行进度写入数据库，实现 Reporter 接口。
// 采用双触发条件的缓冲区策略：BatchComplete() 立即 flush + 定时器安全网。
//
// 目标模型（流水线重构后）：
//   - 进度事实源是 JobRound（资源×轮次）行；Job.progress_total/
//     progress_completed 是矩阵派生的缓存计数器（同一事务增量维护）。
//   - 轮次断点是关系化的 job_round_segments 行（每段一行、纯追加），
//     且是轮次进度的**唯一**事实源：executor 对已解决子集逐段调
//     SegmentResolved 登记，flush 事务内插入断点行并把
//     segment_completed 写成该集合基数——「segment_completed ≡ 断点集合
//     基数」（checkpoint 不变式）因此对全部模式恒成立，含 translate 轮。
//   - 计数列由集合派生而非计数器累加，带来的性质：幂等（重复登记同一段
//     不推进）、收敛（绝对值写入，列值恒向「对齐后的集合基数」收敛，不受
//     写入次数与顺序影响）、自愈（「内存集合 = 已落库 ∪ 待落库」是显式前置
//     状态而非假设：StageStart 与每次 flush 写入前都先以 DB 断点行重建未对齐
//     的集合，对齐不成不写 segment_completed；重扫集合与已解决集合重叠也不会
//     重复计数）。
//     收敛而非单调：列值恒等于断点集合基数时写入只增不减，但列若被非断点来源
//     污染到高于基数（存量数据、外部改数），对齐后的重申会把它拉回基数——那是
//     这套设计要的修复方向，不是回退。真正需要禁止的是「写出低于断点行数的
//     值」，由对齐闸门保证。
//   - segment_completed 由本 Reporter 独占写入：service 层的终态闭合
//     （completed/skipped）不回写这一列，闭合值在读侧按轮次状态派生
//     （service.jobRoundProgress）——列值不被非断点来源污染，这正是它
//     可以安全充当恢复基线的前提。
//   - 无轮次行（单资源路径：preview/quick-translate/cli）时退化为旧语义
//     ——SegmentResolved 为 no-op，仅按 SegmentDone 计数累加 Job 计数器。
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
	mu         sync.Mutex
	stageName  string
	stageTotal int

	// 定时器安全网
	ticker *time.Ticker
	done   chan struct{}
	once   sync.Once

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

// StageStart 记录新轮次开始，将轮次信息写入 JobRound 行并累加 Job.progress_total。
//
// 断点集合对齐无条件先行（rowID > 0 即做，不以 segment_total > 0 为前提）：
// 「segment_total == 0 ⇒ 无断点行」的推断靠不住——首次 StageStart 的行更新
// 可能失败而流程继续（见下方非事务降级分支），后续 flush 照样落断点行，下一
// 次恢复就会看到 segment_total == 0 与已有断点行并存。对齐是写
// segment_completed 的前置条件，不能押在这个赌注上；代价是全新轮次多付一次
// 必然空返的索引查询，换来集合不再依赖任何推断。
//
// 分母累加规则（矩阵不变式）：progress_total 仅在「首次揭示工作量」时累加——
// 行内 segment_total == 0（首次启动）时写入 total 并累加 progress_total；行内
// 已有 segment_total（恢复/重试重跑同一轮）时不覆写、不重复累加。
//
// 基线锚定对齐后的断点集合（而非计数列）的理由：集合是轮次进度的唯一事实源，
// 且它同时就是「本轮无需重做的段」；segment_completed 只是集合基数的派生缓存
// （由本 Reporter 独占写入，终态闭合值不落在这一列），读它做基线会把非断点
// 来源的值混进基线。恢复重跑时集合与 DB 对齐后，重扫集合里的段会被
// SegmentResolved 幂等跳过，故 baseline + 本轮新解决 = 集合基数 ≤ segment_total
// 恒成立——重扫集合与已解决集合重叠（如 QA 自动拒绝的段被 pending_only 重新
// 拾取）也不会重复计数，segment_completed 不会超过分母、也不会因基线归零而
// 倒退。对齐失败（断点查询出错）时基线保持 0：只影响 stageDone 展示计数与
// stage_done 的 SSE 文案，不喂养任何持久化计数器。
// 单资源路径（无轮次行）：无行可对齐，无条件累加，基线 0。
func (r *DBReporter) StageStart(name string, total int) {
	r.flushWriteMu.Lock()
	defer r.flushWriteMu.Unlock()

	r.mu.Lock()
	r.stageName = name
	r.stageTotal = total
	r.mu.Unlock()

	// 重置计数缓冲并快照当前轮次记录（同锁保证快照一致）
	r.flushMu.Lock()
	r.pending = r.pending[:0]
	round := r.round
	r.flushMu.Unlock()

	rowID := 0
	if round != nil {
		rowID = round.rowID
	}

	baseline := 0
	addTotal := total

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if rowID > 0 {
		// 对齐无条件先行（见函数 doc）。失败时 baseline 保持 0，集合留在未
		// 对齐状态，该轮 segment_completed 的写入推迟到对齐成功的那次 flush。
		if n, ok := r.alignResolved(ctx, round); ok {
			baseline = n
		}
		// 读取行内 segment_total，决定首次揭示 vs 恢复重跑。只取该列投影：
		// segment_completed 不作为基线来源（它是集合基数的派生缓存、由本
		// Reporter 独占写入，终态闭合值不落在这一列——读它会把非断点来源
		// 的值混进基线），轮次行其余列每轮一次的读取读出即弃是纯读放大。
		row, err := r.client.JobRound.Query().
			Where(jobround.IDEQ(rowID)).
			Select(jobround.FieldSegmentTotal).
			Only(ctx)
		if err != nil {
			r.logger.Warn("DBReporter: failed to load round row, treating as fresh start",
				"job_id", r.jobID, "round_row_id", rowID, "error", err)
		} else if row.SegmentTotal > 0 {
			// 恢复/重试重跑：保留原 total，不重复累加分母。
			addTotal = 0
		}
	}
	// 对齐失败时 baseline 为 0：stageDone 只被 StageDone 的 SSE 消息文本读取，
	// 不喂养任何持久化计数器。
	r.stageDone.Store(int64(baseline))

	now := time.Now()
	// 事务包裹 JobRound 行更新与 Job 的 progress_total 累加，避免单条失败
	// 导致矩阵与计数器缓存偏离。
	tx, err := r.client.Tx(ctx)
	if err != nil {
		r.logger.Warn("DBReporter: failed to begin stage tx, falling back to non-transactional write",
			"job_id", r.jobID,
			"job_resource_id", r.jobResourceID,
			"stage", name,
			"error", err)
		// 降级为非事务写入：至少保证计数器尽量一致。
		// 与事务路径同守卫：恢复/重跑（addTotal==0）不覆写行内 segment_total。
		if rowID > 0 {
			roundUpdate := r.client.JobRound.UpdateOneID(rowID).
				SetNillableStartedAt(&now)
			if addTotal > 0 {
				roundUpdate = roundUpdate.SetSegmentTotal(total)
			}
			if err := roundUpdate.Exec(ctx); err != nil {
				r.logger.Warn("DBReporter: fallback failed to update round row",
					"job_id", r.jobID,
					"round_row_id", rowID,
					"error", err)
			}
		}
		if addTotal > 0 {
			if err := r.client.Job.UpdateOneID(r.jobID).
				AddProgressTotal(int64(addTotal)).
				Exec(ctx); err != nil {
				r.logger.Warn("DBReporter: fallback failed to add job progress_total",
					"job_id", r.jobID,
					"total", addTotal,
					"error", err)
			}
		}
	} else {
		defer func() {
			_ = tx.Rollback()
		}()
		committed := false
		if rowID > 0 {
			roundUpdate := tx.JobRound.UpdateOneID(rowID).
				SetNillableStartedAt(&now)
			if addTotal > 0 {
				// 首次揭示：写入 total（恢复重跑时保留原值不覆写）。
				roundUpdate = roundUpdate.SetSegmentTotal(total)
			}
			if err := roundUpdate.Exec(ctx); err != nil {
				r.logger.Warn("DBReporter: failed to update round row",
					"job_id", r.jobID,
					"round_row_id", rowID,
					"error", err)
			} else {
				committed = true
			}
		} else {
			committed = true
		}
		if committed && addTotal > 0 {
			if err := tx.Job.UpdateOneID(r.jobID).
				AddProgressTotal(int64(addTotal)).
				Exec(ctx); err != nil {
				r.logger.Warn("DBReporter: failed to add job progress_total",
					"job_id", r.jobID,
					"total", addTotal,
					"error", err)
			} else if err := tx.Commit(); err != nil {
				r.logger.Warn("DBReporter: failed to commit stage tx",
					"job_id", r.jobID,
					"stage", name,
					"error", err)
			}
		} else if committed {
			if err := tx.Commit(); err != nil {
				r.logger.Warn("DBReporter: failed to commit stage tx",
					"job_id", r.jobID,
					"stage", name,
					"error", err)
			}
		}
	}

	// Publish stage_start event
	r.publishEvent("stage_start", name, fmt.Sprintf("轮次开始: %s (%d 段)", name, total))
}

// alignResolved 以 DB 断点行为准对齐该轮内存集合，返回对齐后的集合基数与成败。
// 对齐 = 把 resolved 重建为「已落库 ∪ pending 残余」而非往旧集合里并：既不在
// DB 也不在 pending 的成员没有任何落库路径，保留只会虚增基数；pending 中已
// 落库的 ID（上一轮运行已写入的恢复重扫段）剔除。查询不持 flushMu（DB IO 不
// 阻塞 SegmentResolved 登记）；查询与加锁之间到达的登记落进 pending，重建时
// 一并拾取，不丢。对齐成功后该轮才具备写 segment_completed 的资格（needsWrite
// 的计数分支自此可信）。
//
// 失败时集合保持未对齐、本轮进度写入被推迟：调用方把基线按 0 处理（仅展示），
// 真正的修复在下次 flush 前的 alignCheckpoints 重试。
func (r *DBReporter) alignResolved(ctx context.Context, c *roundCheckpoint) (int, bool) {
	ids, err := r.client.JobRoundSegment.Query().
		Where(jobroundsegment.JobRoundIDEQ(c.rowID)).
		Select(jobroundsegment.FieldSegmentID).
		Ints(ctx)
	if err != nil {
		r.logger.Warn("DBReporter: failed to load round checkpoints, round progress write deferred until aligned",
			"job_id", r.jobID, "round_row_id", c.rowID, "error", err)
		return 0, false
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
	return len(c.resolved), true
}

// alignCheckpoints 把所有未对齐的轮次断点集合与 DB 对齐（flush 写
// segment_completed 的前置条件）：从 stale 与当前轮收集未对齐者逐个重建。
// 无轮次行（round 与 stale 皆空）时自然为 no-op——flushFn 测试注入路径因此
// 保持无 DB。单个对齐失败不阻断其余轮次：失败者保持未对齐、不产出写入，
// 留待下次 flush 重试。
func (r *DBReporter) alignCheckpoints() {
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
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, c := range unaligned {
		r.alignResolved(ctx, c)
	}
}

// SegmentDone 记录一个段落完成：推进轮次展示计数，并追加计数缓冲。
// 计数缓冲只在无轮次行的退化路径参与 Job.progress_completed 累加——
// 有轮次行时进度增量由断点集合基数派生（见 flush）。
func (r *DBReporter) SegmentDone() {
	cur := r.stageDone.Add(1)

	r.flushMu.Lock()
	r.pending = append(r.pending, segmentUpdate{done: cur})
	r.flushMu.Unlock()
}

// BatchComplete 批次完成时调用，立即触发缓冲区 flush。
func (r *DBReporter) BatchComplete() {
	r.flush()
}

// StageDone 记录当前轮次完成。
func (r *DBReporter) StageDone() {
	// 轮次结束时做一次最终 flush，确保所有进度写入 DB
	r.flush()

	r.mu.Lock()
	stageName := r.stageName
	done := r.stageDone.Load()
	r.mu.Unlock()

	// Publish stage_done event
	r.publishEvent("stage_done", stageName, fmt.Sprintf("轮次完成: %s (%d 段)", stageName, done))
}

// Close 释放资源，停止定时器，做最后一次 flush。
func (r *DBReporter) Close() error {
	var err error
	r.once.Do(func() {
		close(r.done)
		r.ticker.Stop()
		// 最后一次 flush
		err = r.flush()
		r.reportCheckpointResidue()

		// Publish final event
		r.publishEvent("stage_done", "", "资源处理完成")
	})
	return err
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
	// 对齐是写 segment_completed 的前置条件：未对齐的轮次先以 DB 为准重建
	// 集合。无轮次行时该步自然为 no-op，flushFn 测试注入路径因此保持无 DB。
	r.alignCheckpoints()

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

	// DB IO 期间释放 flushMu：写入用的是快照（ids 与绝对 count 都已定格），
	// 与其后到达的 SegmentResolved 无关——新段进 resolved 也进 pending，本次
	// 写完后 DB 集合恰为快照 count，新段留待下次 flush 连带更大的 count 一起
	// 落库，checkpoint 不变式在每个提交点都成立。持锁跨事务只会让收集点的
	// SegmentDone/SegmentResolved 阻塞整个事务时长（DB 变慢时最多 5s）。
	// flush 生命周期本身由 flushWriteMu 串行化，切轮与 StageStart 不会插进来。
	r.flushMu.Unlock()

	err := r.writeFlush(writes, jobDelta)
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
		// 唯一索引冲突 = 内存集合已偏离 DB（对齐失败，或「事务已提交但客户端
		// 认为失败」的边界）：标记未对齐，下次 flush 前由 alignCheckpoints 以
		// DB 为准重建集合再写——不重对齐会让该轮 flush 每次都撞同一行、永久
		// 失败。
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

// writeFlush 真实写入路径（单事务）：
//  1. 断点增量：逐轮 CreateBulk 追加 job_round_segments 行（纯计数申明的
//     条目无增量，跳过插入）；
//  2. 段计数：SetSegmentCompleted(断点集合基数)（绝对值写入，幂等且崩溃安全）；
//  3. Job 计数：AddProgressCompleted(各轮基数增量之和 + 退化路径计数)。
//
// 事务保证任意崩溃点「segment_completed ≡ 断点集合基数」（checkpoint 不变式），
// 且 Job 派生计数器与矩阵求和一致（ReconcileJob 重算不会跳变）。
//
// 关于冲突忽略：schema 注释约定写入方用 CreateBulk + OnConflict Ignore，但本项目
// ent 生成代码未启用 sql/upsert feature（无 OnConflict API），故去重下移到内存
// 集合（对齐重建 + SegmentResolved 入缓冲即去重）；(job_round_id, segment_id)
// 唯一索引兜底残余边界，撞到即标记该轮未对齐，下次 flush 前以 DB 为准重建
// 集合再写。
func (r *DBReporter) writeFlush(writes []checkpointWrite, jobDelta int) error {
	delta := jobDelta
	for _, w := range writes {
		delta += len(w.ids)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := r.client.Tx(ctx)
	if err != nil {
		r.logger.Warn("DBReporter: failed to begin flush tx",
			"job_id", r.jobID,
			"job_resource_id", r.jobResourceID,
			"error", err)
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, w := range writes {
		// 纯计数申明的条目（ids 为空）只重申 segment_completed，不插入。
		if len(w.ids) > 0 {
			builders := make([]*ent.JobRoundSegmentCreate, 0, len(w.ids))
			for _, id := range w.ids {
				builders = append(builders, tx.JobRoundSegment.Create().
					SetJobRoundID(w.round.rowID).
					SetSegmentID(id))
			}
			if err := tx.JobRoundSegment.CreateBulk(builders...).Exec(ctx); err != nil {
				r.logger.Warn("DBReporter: failed to insert checkpoint rows",
					"job_id", r.jobID,
					"round_row_id", w.round.rowID,
					"count", len(builders),
					"error", err)
				return err
			}
		}
		if err := tx.JobRound.UpdateOneID(w.round.rowID).
			SetSegmentCompleted(w.count).
			Exec(ctx); err != nil {
			r.logger.Warn("DBReporter: failed to update round progress",
				"job_id", r.jobID,
				"round_row_id", w.round.rowID,
				"error", err)
			return err
		}
	}

	if delta > 0 {
		if err := tx.Job.UpdateOneID(r.jobID).
			AddProgressCompleted(int64(delta)).
			Exec(ctx); err != nil {
			r.logger.Warn("DBReporter: failed to add job progress_completed",
				"job_id", r.jobID,
				"delta", delta,
				"error", err)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		r.logger.Warn("DBReporter: failed to commit flush tx",
			"job_id", r.jobID,
			"error", err)
		return err
	}
	return nil
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
		CreatedAt: time.Now(),
	})
}

// OnBatchEvent implements BatchObserver. Publishes batch events to the Broker.
func (r *DBReporter) OnBatchEvent(batchEvent BatchEvent) {
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
	r.broker.Publish(r.jobID, event.Event{
		Type:      "batch",
		JobID:     r.jobID,
		Level:     BatchLevelFromStatus(batchEvent.Status),
		Stage:     batchEvent.Stage,
		Message:   fmt.Sprintf("batch (%d segs): %s", batchEvent.SegmentCount, batchEvent.Status),
		Metadata:  metadata,
		CreatedAt: time.Now(),
	})
}

// OnPoolEvent implements PoolObserver. Publishes pool-level events to the Broker.
// pool_advance 用 warn 级（仍有未解决段），pool_start 用 info 级。
// shrink=1.0（不缩）时用"重切"措辞；shrink<1.0（缩比）时用"缩批/缩放"措辞。
func (r *DBReporter) OnPoolEvent(poolEvent PoolEvent) {
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
		CreatedAt: time.Now(),
	})
}
