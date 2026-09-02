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
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

// segmentUpdate 记录一次 SegmentDone 事件的状态。
type segmentUpdate struct {
	done int64 // 当前轮次已完成的段落数（原子值快照）
}

// DBReporter 将执行进度写入数据库，实现 Reporter 接口。
// 采用双触发条件的缓冲区策略：BatchComplete() 立即 flush + 定时器安全网。
//
// 目标模型（流水线重构后）：
//   - 进度事实源是 JobRound（资源×轮次）行；StageStart/SegmentDone 写入
//     当前行（roundRowID），Job.progress_total/progress_completed 是
//     矩阵派生的缓存计数器（同一事务增量维护）。
//   - roundRowID 为 0（单资源路径：preview/quick-translate/cli）时退化为
//     旧语义——仅累加 Job 计数器。
//
// 并发约定：flushMu 保护「缓冲区 + 当前轮次行 + 断点源」三者的快照一致性
// （ticker goroutine 与调用方 goroutine 交叉 flush 时不出现「旧缓冲写新行」）；
// mu 保护 stageName/stageTotal 展示态；stageDone 为原子计数。
type DBReporter struct {
	client        *ent.Client
	jobID         int
	jobResourceID int
	// roundRowID 当前活跃轮次的 JobRound 行 ID；0 表示无轮次行
	//（单资源路径，progress 只进 Job 计数器）。受 flushMu 保护。
	roundRowID int
	logger     *slog.Logger
	broker     *event.Broker

	// resolvedSource 由调用方注入：返回当前轮次应持久化的断点集合
	//（DB Segment ID 列表）。nil 时不持久化断点（translate 轮恒为 nil）。
	// 受 flushMu 保护。
	resolvedSource func() []int

	// resolvedDirty 断点脏标记。轮次执行期间 resolvedByMode 恒定
	//（AccumulateResolved 仅在轮末执行），轮内重复 flush 无信息增量，
	// 每 2s 全量重写同一 JSON blob 只产生写放大。安装新源时置脏，
	// 首次 flush 搭载写入后清除；PersistResolved 无条件写入。受 flushMu 保护。
	resolvedDirty bool

	// 轮次展示态
	mu         sync.Mutex
	stageName  string
	stageTotal int

	// 缓冲区：SegmentDone 只追加，flush 时批量写入 DB。受 flushMu 保护。
	pending []segmentUpdate
	flushMu sync.Mutex

	// 轮次内段完成计数
	stageDone atomic.Int64

	// 定时器安全网
	ticker *time.Ticker
	done   chan struct{}
	once   sync.Once

	// flush 函数，方便测试注入
	flushFn func([]segmentUpdate) error
}

// DBReporterOptions 是 DBReporter 的配置选项。
// 轮次目标行与断点源经 SwitchRound/SetResolvedSource 运行时注入
// （每轮切换，单一注入通道）。
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

// SwitchRound 切换当前轮次行（轮次边界时由 runner 调用）。
// 上一轮残余缓冲先 flush（写入旧行），再切换目标行并重置段计数。
func (r *DBReporter) SwitchRound(roundRowID int) {
	r.flush()
	r.flushMu.Lock()
	r.roundRowID = roundRowID
	r.flushMu.Unlock()
	r.stageDone.Store(0)
}

// SetResolvedSource 切换断点集合源（每轮由 runner 调用）。
// nil 表示本轮不持久化断点（translate 轮由 Segment.status 驱动增量）。
// 切换即置脏：新集合至少写一次（保证轮末 AccumulateResolved 后的
// PersistResolved 之前，ticker flush 已把当前集合落盘）。
func (r *DBReporter) SetResolvedSource(fn func() []int) {
	r.flushMu.Lock()
	r.resolvedSource = fn
	r.resolvedDirty = true
	r.flushMu.Unlock()
}

// StageStart 记录新轮次开始，将轮次信息写入 JobRound 行并累加 Job.progress_total。
//
// 分母累加规则（矩阵不变式）：progress_total 仅在「首次揭示工作量」时累加——
// 行内 segment_total == 0（首次启动）时写入 total 并累加 progress_total，段计数
// 基线为 0；行内已有 segment_total（恢复/重试重跑同一轮）时不覆写、不重复累加，
// 段计数基线取行内保留的 segment_completed（恢复时保留已 flush 值继续累加）。
// 单资源路径（roundRowID==0）：无行可读，无条件累加，基线 0。
func (r *DBReporter) StageStart(name string, total int) {
	r.mu.Lock()
	r.stageName = name
	r.stageTotal = total
	r.mu.Unlock()

	// 重置缓冲区并快照当前目标行（同锁保证快照一致）
	r.flushMu.Lock()
	r.pending = r.pending[:0]
	rowID := r.roundRowID
	r.flushMu.Unlock()

	baseline := 0
	addTotal := total

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if rowID > 0 {
		// 读取行内现有计数，决定首次揭示 vs 恢复重跑。
		// 投影只取两个计数器列：轮次行含可达几十 KB 的断点 blob，
		// 每轮一次的读取读出即弃是纯读放大。
		row, err := r.client.JobRound.Query().
			Where(jobround.IDEQ(rowID)).
			Select(jobround.FieldSegmentTotal, jobround.FieldSegmentCompleted).
			Only(ctx)
		if err != nil {
			r.logger.Warn("DBReporter: failed to load round row, treating as fresh start",
				"job_id", r.jobID, "round_row_id", rowID, "error", err)
		} else if row.SegmentTotal > 0 {
			// 恢复/重试重跑：保留原 total，不重复累加分母；
			// 基线取保留的已完成数，继续累加。
			addTotal = 0
			baseline = row.SegmentCompleted
		}
	}
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

// SegmentDone 记录一个段落完成，仅追加到缓冲区，不直接触发 DB 写入。
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

		// Publish final event
		r.publishEvent("stage_done", "", "资源处理完成")
	})
	return err
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

// flush 取出缓冲区所有待处理更新并执行写入。
// 同一临界区快照「缓冲 + 目标行 + 断点数据」，保证并发 flush（ticker 与
// 调用方 goroutine 交叉）时不会出现「旧缓冲写新行」的错位。
// 断点数据在临界区内解析：闭包读 resolvedMu 下的可变 mode/映射，锁外延迟
// 调用会读到轮次切换后的新 mode（跨轮竞态会把错误模式的集合写进旧行）；
// 锁序 flushMu→resolvedMu 单向，无反向持锁调用。
// 断点集合仅在脏标记置位时搭载（轮内集合恒定，重复重写无信息增量）；
// pending 空也放行（dirty 即检查点待写），写入成功清脏，失败保脏重试。
func (r *DBReporter) flush() error {
	r.flushMu.Lock()
	writeResolved := r.resolvedDirty && r.resolvedSource != nil && r.roundRowID > 0
	if len(r.pending) == 0 && !writeResolved {
		r.flushMu.Unlock()
		return nil
	}
	updates := r.pending
	r.pending = make([]segmentUpdate, 0, len(updates))
	rowID := r.roundRowID
	var resolved []int
	if writeResolved {
		resolved = r.resolvedSource()
	}
	r.flushMu.Unlock()

	var err error
	if r.flushFn != nil {
		err = r.flushFn(updates)
	} else {
		err = r.writeFlush(updates, rowID, resolved, writeResolved)
	}
	if err == nil && writeResolved {
		r.flushMu.Lock()
		r.resolvedDirty = false
		r.flushMu.Unlock()
	}
	return err
}

// PersistResolved 强制持久化断点集合：轮次成功结束（AccumulateResolved 后）
// 但缓冲区为空时，常规 flush 会跳过，本轮新增的 resolved 段将不落盘——
// 暂停/崩溃恢复后会被全量重扫（重复 LLM 调用与重复计费）。
// runner 在轮次收尾时调用本方法兜底。无轮次行或不持久化断点（translate）时为 no-op。
// 写入失败时置脏，由 flush 的 dirty 放行路径重试（轮末断点是恢复正确性兜底）。
func (r *DBReporter) PersistResolved() {
	r.flushMu.Lock()
	if r.roundRowID <= 0 || r.resolvedSource == nil {
		r.flushMu.Unlock()
		return
	}
	updates := r.pending
	r.pending = make([]segmentUpdate, 0, len(updates))
	rowID := r.roundRowID
	// 临界区内解析断点数据（同 flush 的锁序与跨轮竞态理由）。
	resolved := r.resolvedSource()
	r.flushMu.Unlock()

	if r.flushFn != nil {
		_ = r.flushFn(updates)
		return
	}
	if err := r.writeFlush(updates, rowID, resolved, true); err != nil {
		r.flushMu.Lock()
		r.resolvedDirty = true
		r.flushMu.Unlock()
		return
	}
	// 强制写入成功后清脏，与 flush 的搭载路径收敛到同一脏标记。
	r.flushMu.Lock()
	r.resolvedDirty = false
	r.flushMu.Unlock()
}

// writeFlush 真实写入路径：更新 JobRound.segment_completed，并累加
// Job.progress_completed。同一事务内（writeResolved 时）持久化断点集合，
// 保证计数器与断点的提交一致性。
// updates 可为空（PersistResolved 强制断点写入 / dirty 重试路径）：
// 此时跳过段计数与 Job 计数器写入（已由常规 flush 持久化），仅写断点集合。
func (r *DBReporter) writeFlush(updates []segmentUpdate, rowID int, resolved []int, writeResolved bool) error {
	if len(updates) == 0 && (rowID <= 0 || !writeResolved) {
		return nil
	}

	// 取最后一个 update 的 done 值作为当前轮次完成数（空缓冲时无段计数写入）
	var lastDone int64
	delta := len(updates)
	if delta > 0 {
		lastDone = updates[len(updates)-1].done
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

	if rowID > 0 {
		// 更新 JobRound 行的段完成数（绝对值写入，幂等且崩溃安全）
		roundUpdate := tx.JobRound.UpdateOneID(rowID)
		if delta > 0 {
			roundUpdate = roundUpdate.SetSegmentCompleted(int(lastDone))
		}
		// 断点集合搭载同一事务：writeResolved=false 时跳过（translate 轮/非脏）。
		if writeResolved {
			roundUpdate = roundUpdate.SetResolvedSegmentIds(resolved)
		}
		if err := roundUpdate.Exec(ctx); err != nil {
			r.logger.Warn("DBReporter: failed to update round progress",
				"job_id", r.jobID,
				"round_row_id", rowID,
				"error", err)
			return err
		}
	}

	// 累加 Job 级工作量完成数
	if delta == 0 {
		// 纯断点写入路径：无计数器增量，避免空更新。
		if err := tx.Commit(); err != nil {
			r.logger.Warn("DBReporter: failed to commit flush tx",
				"job_id", r.jobID,
				"error", err)
			return err
		}
		return nil
	}
	if err := tx.Job.UpdateOneID(r.jobID).
		AddProgressCompleted(int64(delta)).
		Exec(ctx); err != nil {
		r.logger.Warn("DBReporter: failed to add job progress_completed",
			"job_id", r.jobID,
			"delta", delta,
			"error", err)
		return err
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
