package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// parseSSECursor parses a Last-Event-ID value into a non-negative seq.
// On parse failure or negative value it falls back to 0 (full replay) and
// logs a warning, so a malformed/garbage client cursor never yields a
// negative SeqGT filter (which would replay every event).
func (s *Server) parseSSECursor(raw, source string) int64 {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		s.logger.Warn("job_stream: invalid Last-Event-ID, falling back to full replay",
			"source", source, "raw", raw)
		return 0
	}
	return v
}

var sseEventTypeReplacer = strings.NewReplacer("\r", "", "\n", "")

// StreamJobEvents 实现 OpenAPI 生成的 ServerInterface（GET /jobs/{jobId}/stream）。
// params 中的 lastEventId 与 handleJobStream 内部 query/header 读取语义一致
// （EventSource 无法设置自定义 header，前端经 query 兜底），内部读取逻辑保持不变。
func (s *Server) StreamJobEvents(w http.ResponseWriter, r *http.Request, jobId JobId, _ StreamJobEventsParams) {
	s.handleJobStream(w, r, int(jobId))
}

func (s *Server) handleJobStream(w http.ResponseWriter, r *http.Request, jobID int) {
	s.handleJobStreamWithInterval(w, r, jobID, 5*time.Second)
}

func (s *Server) handleJobStreamWithInterval(w http.ResponseWriter, r *http.Request, jobID int, checkInterval time.Duration) {
	authUser, err := s.resolveAuthUser(r)
	if err != nil {
		s.writeAuthProblem(w, r, err)
		return
	}
	if err := s.jobSvc.CheckJobAccess(r.Context(), authUser.User.ID, jobID); err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}
	checkAccess := func() bool {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		return s.jobSvc.CheckJobAccess(ctx, authUser.User.ID, jobID) == nil
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := s.eventBroker.Subscribe(jobID)
	defer s.eventBroker.Unsubscribe(jobID, ch)

	controller := http.NewResponseController(w)
	// Bound a slow client's write so it cannot indefinitely defer authorization checks.
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// 从 ring buffer 回放历史事件
	var lastSeq int64
	var afterSeq int64
	if lastEventIDStr := r.Header.Get("Last-Event-ID"); lastEventIDStr != "" {
		// 重连：从上次断开的位置继续
		afterSeq = s.parseSSECursor(lastEventIDStr, "Last-Event-ID header")
	} else if q := r.URL.Query().Get("lastEventId"); q != "" {
		// 原生 EventSource 无法设置自定义 header，前端通过 query 兜底传 Last-Event-ID
		afterSeq = s.parseSSECursor(q, "lastEventId query")
	}
	batchSize := s.sseReplayBatch
	if batchSize <= 0 {
		batchSize = 200
	}
	maxReplay := s.sseMaxReplay
	if maxReplay <= 0 {
		maxReplay = 512
	}
	// 新连接（无 Last-Event-ID）：SSE 只负责「实时 + 最近窗口补进」，历史全量走 REST。
	// 将回放起点前移到最近 maxReplay 条，避免从 seq 0 升序重放最旧事件。
	if afterSeq == 0 {
		lookupCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		latest, ok := s.eventBroker.LatestSeq(lookupCtx, jobID)
		lookupErr := lookupCtx.Err()
		cancel()
		if lookupErr != nil || !checkAccess() {
			return
		}
		if ok && latest > int64(maxReplay) {
			afterSeq = latest - int64(maxReplay)
		}
	}
	replayed := 0
	for {
		// 分批流式回放：每批从 DB/Ring 拉取 batchSize 条，边写边推，
		// 显著降低首字节时间，并缩短回放窗口以降低竞态丢事件概率。
		remaining := maxReplay - replayed
		if remaining <= 0 {
			break
		}
		thisBatch := batchSize
		if thisBatch > remaining {
			thisBatch = remaining
		}
		replayCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		batch := s.eventBroker.Replay(replayCtx, jobID, afterSeq, thisBatch)
		replayErr := replayCtx.Err()
		cancel()
		if replayErr != nil || !checkAccess() {
			return
		}
		if len(batch) == 0 {
			break
		}
		for _, evt := range batch {
			if ctxErr := r.Context().Err(); ctxErr != nil {
				// 客户端断开，早停
				return
			}
			if !checkAccess() {
				return
			}
			lastSeq = evt.Seq
			afterSeq = evt.Seq
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", evt.Seq, sseEventTypeReplacer.Replace(evt.Type), string(data)); err != nil {
				return
			}
		}
		replayed += len(batch)
		flusher.Flush()
		if len(batch) < thisBatch {
			break // 无更多历史
		}
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if evt.Seq <= lastSeq {
				continue
			}
			if !checkAccess() {
				return
			}
			lastSeq = evt.Seq
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", lastSeq, sseEventTypeReplacer.Replace(evt.Type), string(data)); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if !checkAccess() {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix()); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
