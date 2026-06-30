package progress

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

// segmentUpdate 记录一次 SegmentDone 事件的状态。
type segmentUpdate struct {
	done int64 // 当前阶段已完成的段落数（原子值快照）
}

// DBReporter 将翻译进度写入数据库，实现 Reporter 接口。
// 采用双触发条件的缓冲区策略：BatchComplete() 立即 flush + 定时器安全网。
type DBReporter struct {
	client        *ent.Client
	jobID         int
	jobResourceID int
	logger        *slog.Logger
	broker        *event.Broker

	// 阶段状态
	mu         sync.Mutex
	stageName  string
	stageTotal int

	// 缓冲区：SegmentDone 只追加，flush 时批量写入 DB
	pending []segmentUpdate
	flushMu sync.Mutex

	// 阶段内段完成计数
	stageDone atomic.Int64

	// 定时器安全网
	ticker *time.Ticker
	done   chan struct{}
	once   sync.Once

	// flush 函数，方便测试注入
	flushFn func([]segmentUpdate) error
}

// DBReporterOptions 是 DBReporter 的配置选项。
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
	r.flushFn = r.defaultFlush

	go r.runTicker()

	return r
}

// StageStart 记录新阶段开始，立即将阶段信息写入 JobResource。
func (r *DBReporter) StageStart(name string, total int) {
	r.mu.Lock()
	r.stageName = name
	r.stageTotal = total
	r.stageDone.Store(0)
	r.mu.Unlock()

	// 重置缓冲区
	r.flushMu.Lock()
	r.pending = r.pending[:0]
	r.flushMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now()
	err := r.client.JobResource.UpdateOneID(r.jobResourceID).
		SetCurrentStage(name).
		SetStageTotal(total).
		SetStageCompleted(0).
		SetNillableStartedAt(&now).
		Exec(ctx)
	if err != nil {
		r.logger.Warn("DBReporter: failed to update stage info",
			"job_id", r.jobID,
			"job_resource_id", r.jobResourceID,
			"stage", name,
			"error", err)
	}

	// Publish stage_start event
	r.publishEvent("stage_start", name, fmt.Sprintf("阶段开始: %s (%d 段)", name, total))
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

// StageDone 记录当前阶段完成。
func (r *DBReporter) StageDone() {
	// 阶段结束时做一次最终 flush，确保所有进度写入 DB
	r.flush()

	r.mu.Lock()
	stageName := r.stageName
	done := r.stageDone.Load()
	r.mu.Unlock()

	// Publish stage_done event
	r.publishEvent("stage_done", stageName, fmt.Sprintf("阶段完成: %s (%d 段)", stageName, done))
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
		r.publishEvent("stage_done", "", "资源翻译完成")
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
func (r *DBReporter) flush() error {
	r.flushMu.Lock()
	if len(r.pending) == 0 {
		r.flushMu.Unlock()
		return nil
	}
	updates := r.pending
	r.pending = make([]segmentUpdate, 0, len(updates))
	r.flushMu.Unlock()

	if r.flushFn == nil {
		return r.defaultFlush(updates)
	}
	return r.flushFn(updates)
}

// defaultFlush 默认 flush 实现：更新 JobResource 的 stage_completed 和 TranslationJob 的 completed_segments。
func (r *DBReporter) defaultFlush(updates []segmentUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	// 取最后一个 update 的 done 值作为当前阶段完成数
	lastDone := updates[len(updates)-1].done
	delta := len(updates)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 更新 JobResource 的 stage_completed
	err := r.client.JobResource.UpdateOneID(r.jobResourceID).
		SetStageCompleted(int(lastDone)).
		Exec(ctx)
	if err != nil {
		r.logger.Warn("DBReporter: failed to update job resource progress",
			"job_id", r.jobID,
			"job_resource_id", r.jobResourceID,
			"error", err)
		return err
	}

	// 按缓冲区长度增量更新 TranslationJob 的 completed_segments
	return r.updateJobProgress(ctx, delta)
}

// updateJobProgress 使用 AddCompletedSegments 原子增量更新 TranslationJob 的完成段落数。
func (r *DBReporter) updateJobProgress(ctx context.Context, delta int) error {
	err := r.client.TranslationJob.UpdateOneID(r.jobID).
		AddCompletedSegments(delta).
		Exec(ctx)
	if err != nil {
		r.logger.Warn("DBReporter: failed to update job progress",
			"job_id", r.jobID,
			"delta", delta,
			"error", err)
	}
	return err
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
	if batchEvent.ErrorType != "" {
		metadata["error_type"] = batchEvent.ErrorType
	}
	if batchEvent.ErrorMessage != "" {
		metadata["error_message"] = batchEvent.ErrorMessage
	}
	if batchEvent.HTTPStatus > 0 {
		metadata["http_status"] = batchEvent.HTTPStatus
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
