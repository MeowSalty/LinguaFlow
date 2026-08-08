package event

import (
	"context"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sseevent"
)

type EntEventStore struct {
	client *ent.Client
}

func NewEntEventStore(client *ent.Client) *EntEventStore {
	return &EntEventStore{client: client}
}

func (s *EntEventStore) Append(jobID int, evt Event) (int64, error) {
	ctx := context.Background()
	metadata := map[string]any{}
	if evt.Metadata != nil {
		metadata = evt.Metadata
	}
	create := s.client.SSEEvent.Create().
		SetJobID(jobID).
		SetSeq(evt.Seq).
		SetType(evt.Type).
		SetLevel(evt.Level).
		SetMessage(evt.Message).
		SetNillableStage(strPtr(evt.Stage)).
		SetMetadata(metadata).
		SetCreatedAt(evt.CreatedAt)
	if _, err := create.Save(ctx); err != nil {
		slog.Error("ent_event_store: append failed", "job_id", jobID, "seq", evt.Seq, "error", err)
		return evt.Seq, err
	}
	return evt.Seq, nil
}

// Replay returns up to limit events with seq > afterSeq for the given job,
// ordered ascending by seq. If limit <= 0, all matching events are returned.
// The ctx cancels the underlying DB query on client disconnect.
func (s *EntEventStore) Replay(ctx context.Context, jobID int, afterSeq int64, limit int) []Event {
	rows, err := s.queryEvents(ctx, jobID, afterSeq, limit).All(ctx)
	if err != nil {
		slog.Error("ent_event_store: replay failed", "job_id", jobID, "after_seq", afterSeq, "limit", limit, "error", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	return rowsToEvents(rows)
}

// ListPage queries up to limit events with seq > afterSeq, ordered ascending by
// seq, and returns a pagination result. It fetches limit+1 rows to determine
// hasMore without a second query; when hasMore is true, nextAfterSeq is set to
// the last returned event's seq as the cursor for the next page.
// limit must be > 0.
func (s *EntEventStore) ListPage(ctx context.Context, jobID int, afterSeq int64, limit int) ([]Event, int64, bool) {
	rows, err := s.queryEvents(ctx, jobID, afterSeq, limit+1).All(ctx)
	if err != nil {
		slog.Error("ent_event_store: list page failed", "job_id", jobID, "after_seq", afterSeq, "limit", limit, "error", err)
		return nil, 0, false
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	events := rowsToEvents(rows)
	var nextAfterSeq int64
	if hasMore && len(events) > 0 {
		nextAfterSeq = events[len(events)-1].Seq
	}
	return events, nextAfterSeq, hasMore
}

// queryEvents builds the shared SSEEvent predicate/order used by both Replay
// and ListPage, so the SSE-replay and REST-history query paths cannot drift.
// If limit > 0 a Limit clause is applied.
func (s *EntEventStore) queryEvents(ctx context.Context, jobID int, afterSeq int64, limit int) *ent.SSEEventQuery {
	query := s.client.SSEEvent.Query().
		Where(
			sseevent.JobIDEQ(jobID),
			sseevent.SeqGT(afterSeq),
		).
		Order(ent.Asc(sseevent.FieldSeq))
	if limit > 0 {
		query = query.Limit(limit)
	}
	return query
}

func rowsToEvents(rows []*ent.SSEEvent) []Event {
	events := make([]Event, len(rows))
	for i, r := range rows {
		events[i] = Event{
			Type:      r.Type,
			JobID:     r.JobID,
			Level:     r.Level,
			Stage:     r.Stage,
			Message:   r.Message,
			Metadata:  r.Metadata,
			CreatedAt: r.CreatedAt,
			Seq:       r.Seq,
		}
	}
	return events
}

// ListPageBefore queries up to limit events with seq < beforeSeq for the given
// job, ordered ascending by seq, and returns a backward-pagination result. It
// fetches limit+1 rows to determine hasMore without a second query; when
// hasMore is true, nextBeforeSeq is the oldest returned event's seq, usable as
// the next before_seq cursor to page further back. When beforeSeq <= 0 no
// lower bound is applied (i.e. the most recent events), which serves the
// initial "latest page" for terminal-job timelines.
// limit must be > 0.
func (s *EntEventStore) ListPageBefore(ctx context.Context, jobID int, beforeSeq int64, limit int) ([]Event, int64, bool) {
	query := s.client.SSEEvent.Query().
		Where(sseevent.JobIDEQ(jobID)).
		Order(ent.Desc(sseevent.FieldSeq))
	if beforeSeq > 0 {
		query = query.Where(sseevent.SeqLT(beforeSeq))
	}
	rows, err := query.Limit(limit + 1).All(ctx)
	if err != nil {
		slog.Error("ent_event_store: list page (before) failed", "job_id", jobID, "before_seq", beforeSeq, "limit", limit, "error", err)
		return nil, 0, false
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	events := rowsToEvents(rows)
	reverseEvents(events)
	var nextBeforeSeq int64
	if hasMore && len(events) > 0 {
		nextBeforeSeq = events[0].Seq
	}
	return events, nextBeforeSeq, hasMore
}

func reverseEvents(events []Event) {
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
}

// LatestSeq returns the highest persisted seq for the given job via a MAX
// aggregate, and false when the job has no events. The ctx cancels the
// underlying DB query on client disconnect.
func (s *EntEventStore) LatestSeq(ctx context.Context, jobID int) (int64, bool) {
	maxSeq, err := s.client.SSEEvent.Query().
		Where(sseevent.JobIDEQ(jobID)).
		Aggregate(ent.Max(sseevent.FieldSeq)).
		Int(ctx)
	if err != nil || maxSeq == 0 {
		return 0, false
	}
	return int64(maxSeq), true
}

func (s *EntEventStore) Purge(jobID int) {
	ctx := context.Background()
	deleted, err := s.client.SSEEvent.Delete().
		Where(sseevent.JobIDEQ(jobID)).
		Exec(ctx)
	if err != nil {
		slog.Error("ent_event_store: purge failed", "job_id", jobID, "error", err)
		return
	}
	slog.Debug("ent_event_store: purged events", "job_id", jobID, "count", deleted)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
