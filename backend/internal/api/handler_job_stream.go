package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
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

func (s *Server) handleJobStream(w http.ResponseWriter, r *http.Request) {
	authUser, err := s.resolveAuthUser(r)
	if err != nil {
		s.writeAuthProblem(w, r, err)
		return
	}
	jobID, ok := s.parseIntParam(w, r, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	if err := s.jobSvc.CheckJobAccess(r.Context(), authUser.User.ID, jobID); err != nil {
		s.writeJobServiceError(w, r, err)
		return
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

	fmt.Fprintf(w, ": connected\n\n")
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
		if latest, ok := s.eventBroker.LatestSeq(r.Context(), jobID); ok && latest > int64(maxReplay) {
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
		batch := s.eventBroker.Replay(r.Context(), jobID, afterSeq, thisBatch)
		if len(batch) == 0 {
			break
		}
		for _, evt := range batch {
			if ctxErr := r.Context().Err(); ctxErr != nil {
				// 客户端断开，早停
				return
			}
			lastSeq = evt.Seq
			afterSeq = evt.Seq
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", evt.Seq, sseEventTypeReplacer.Replace(evt.Type), string(data))
		}
		replayed += len(batch)
		flusher.Flush()
		if len(batch) < thisBatch {
			break // 无更多历史
		}
	}

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
			lastSeq = evt.Seq
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", lastSeq, sseEventTypeReplacer.Replace(evt.Type), string(data))
			flusher.Flush()
		case <-time.After(30 * time.Second):
			fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix())
			flusher.Flush()
		}
	}
}
