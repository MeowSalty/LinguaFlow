// Package progress 暴露翻译流水线进度回调接口与几种实现。
// CLI 层根据 stderr 是否 TTY 决定使用 Terminal（进度条）或 Log（周期 INFO 日志）。
package progress

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/schollz/progressbar/v3"
)

// Reporter 接收翻译流水线发出的进度事件。所有方法必须并发安全。
//
// 生命周期：StageStart → 任意多次 SegmentDone → StageDone，可反复出现。
// total <= 0 表示该阶段无段级进度（如 protect / unprotect），
// 实现可选择只展示阶段开始/结束，跳过段计数。
//
// Close 释放底层资源（如终端进度条），幂等。
type Reporter interface {
	StageStart(name string, total int)
	SegmentDone()
	BatchComplete() // 批次完成时调用，触发缓冲区 flush
	StageDone()
	Close() error
}

// Nop 是无副作用的实现，供测试或 --progress=none 使用。
type Nop struct{}

func (Nop) StageStart(string, int) {}
func (Nop) SegmentDone()           {}
func (Nop) BatchComplete()         {}
func (Nop) StageDone()             {}
func (Nop) Close() error           { return nil }

// BatchEvent describes the result of a single batch translation attempt.
type BatchEvent struct {
	Stage           string                  `json:"stage"`
	SegmentIDs      []string                `json:"segment_ids"`
	SegmentCount    int                     `json:"segment_count"`
	BackendName     string                  `json:"backend_name"`
	Status          string                  `json:"status"` // "success" | "partial" | "failed"
	DurationMs      int64                   `json:"duration_ms"`
	InputTokens     int64                   `json:"input_tokens"`
	OutputTokens    int64                   `json:"output_tokens"`
	SentContent     string                  `json:"sent_content"`
	ReceivedContent string                  `json:"received_content"`
	UsedGlossary    []prompt.GlossaryEntry  `json:"used_glossary,omitempty"`
	AddedGlossary   []prompt.BootstrapEntry `json:"added_glossary,omitempty"`
	ErrorType       string                  `json:"error_type,omitempty"` // "backend_error" | "parse_error" | "placeholder_error" | ""
	ErrorMessage    string                  `json:"error_message,omitempty"`
	HTTPStatus      int                     `json:"http_status,omitempty"`
	TriedBackends   []string                `json:"tried_backends,omitempty"`
	ShrinkAttempted bool                    `json:"shrink_attempted,omitempty"`
}

// BatchObserver is an optional interface that a Reporter may implement
// to receive batch-level events from the translation pipeline.
type BatchObserver interface {
	OnBatchEvent(event BatchEvent)
}

// defaultRefreshEvery 是 refreshLoop 周期重绘的默认间隔。
// 选 250ms：> progressbar 内部 80ms throttle，且足够让 elapsed time 看起来连续。
const defaultRefreshEvery = 250 * time.Millisecond

// terminalReporter 通过 schollz/progressbar/v3 在 TTY 上渲染单条进度条。
// 同一时刻至多一条 bar；StageStart 切换阶段时会 Finish 上一条。
//
// 额外起一个 refreshLoop goroutine 以 refreshEvery 周期调 bar.Add(0)
// 强制重绘，避免在长时间无 SegmentDone 时 elapsed time 静止不动。
type terminalReporter struct {
	w  io.Writer
	mu sync.Mutex
	// bar 仅当当前阶段拥有段级进度（total > 0）时非空。
	bar *progressbar.ProgressBar
	// stageName 用于诊断与渲染描述。
	stageName string

	refreshEvery time.Duration
	done         chan struct{}
	wg           sync.WaitGroup
	closeOnce    sync.Once
}

// NewTerminal 创建终端进度条 Reporter。w 通常是 os.Stderr。
func NewTerminal(w io.Writer) Reporter {
	return newTerminalWithInterval(w, defaultRefreshEvery)
}

// newTerminalWithInterval 是 NewTerminal 的内部入口，仅供测试注入小 interval。
func newTerminalWithInterval(w io.Writer, every time.Duration) *terminalReporter {
	r := &terminalReporter{
		w:            w,
		refreshEvery: every,
		done:         make(chan struct{}),
	}
	r.wg.Add(1)
	go r.refreshLoop()
	return r
}

func (r *terminalReporter) StageStart(name string, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishLocked()
	r.stageName = name
	if total <= 0 {
		// 没有段级进度——仅在 stderr 打一条提示行，不创建 bar。
		fmt.Fprintf(r.w, "▶ %s\n", name)
		return
	}
	r.bar = progressbar.NewOptions(total,
		progressbar.OptionSetWriter(r.w),
		progressbar.OptionSetDescription(fmt.Sprintf("▶ %s", name)),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetItsString("seg"),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetElapsedTime(true),
		progressbar.OptionFullWidth(),
		progressbar.OptionThrottle(80*time.Millisecond),
		progressbar.OptionClearOnFinish(),
		progressbar.OptionOnCompletion(func() { fmt.Fprint(r.w, "\n") }),
		progressbar.OptionSetRenderBlankState(true),
	)
}

func (r *terminalReporter) SegmentDone() {
	r.mu.Lock()
	bar := r.bar
	r.mu.Unlock()
	if bar == nil {
		return
	}
	_ = bar.Add(1)
}

func (r *terminalReporter) BatchComplete() {}

func (r *terminalReporter) StageDone() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishLocked()
}

func (r *terminalReporter) Close() error {
	r.closeOnce.Do(func() {
		close(r.done)
		r.wg.Wait()
		r.mu.Lock()
		r.finishLocked()
		r.mu.Unlock()
	})
	return nil
}

// refreshLoop 周期性强制重绘进度条，避免长时间无 SegmentDone 时画面冻结。
// 锁序：先持 r.mu 取 bar 引用，释锁后再调 bar.Add，避免与库内部锁嵌套。
func (r *terminalReporter) refreshLoop() {
	defer r.wg.Done()
	t := time.NewTicker(r.refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
			r.mu.Lock()
			bar := r.bar
			r.mu.Unlock()
			if bar == nil {
				continue
			}
			// Add(0) 因 OptionShowCount/OptionShowIts 开启而走 render 路径；
			// bar 已 Finish 时库内 state.finished 守卫使 render 早退。
			_ = bar.Add(0)
		}
	}
}

// finishLocked 关闭当前 bar；调用前需持有 r.mu。
func (r *terminalReporter) finishLocked() {
	if r.bar == nil {
		return
	}
	_ = r.bar.Finish()
	r.bar = nil
}

// logReporter 在非 TTY 环境下按时间/段数阈值打 INFO 日志，避免 stderr 静默。
// 触发条件：距离上次输出已过 every 时间 或 已新完成 everyN 段（取先到者）。
type logReporter struct {
	logger *slog.Logger
	every  time.Duration
	everyN int64

	mu        sync.Mutex
	stage     string
	total     int64
	done      atomic.Int64
	lastDone  int64
	lastEmit  time.Time
	stageDone bool
}

// NewLog 创建周期日志 Reporter。every <= 0 视为只按段数阈值；everyN <= 0 视为只按时间。
func NewLog(logger *slog.Logger, every time.Duration, everyN int) Reporter {
	if logger == nil {
		logger = slog.Default()
	}
	return &logReporter{
		logger: logger,
		every:  every,
		everyN: int64(everyN),
	}
}

func (r *logReporter) StageStart(name string, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stage = name
	r.total = int64(total)
	r.done.Store(0)
	r.lastDone = 0
	r.lastEmit = time.Now()
	r.stageDone = false
	if total > 0 {
		r.logger.Info("stage progress",
			"stage", name, "done", 0, "total", total)
	}
}

func (r *logReporter) SegmentDone() {
	cur := r.done.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stageDone {
		return
	}
	now := time.Now()
	byCount := r.everyN > 0 && cur-r.lastDone >= r.everyN
	byTime := r.every > 0 && now.Sub(r.lastEmit) >= r.every
	if !byCount && !byTime {
		return
	}
	r.emitLocked(cur, now)
}

func (r *logReporter) StageDone() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stageDone {
		return
	}
	r.stageDone = true
	cur := r.done.Load()
	// 阶段结尾兜底一条总结，含本阶段最终段数。
	if r.total > 0 {
		r.logger.Info("stage done",
			"stage", r.stage, "done", cur, "total", r.total)
	} else {
		r.logger.Info("stage done", "stage", r.stage)
	}
}

func (r *logReporter) BatchComplete() {}

func (r *logReporter) Close() error { return nil }

func (r *logReporter) emitLocked(cur int64, now time.Time) {
	r.lastDone = cur
	r.lastEmit = now
	r.logger.Info("stage progress",
		"stage", r.stage, "done", cur, "total", r.total)
}
