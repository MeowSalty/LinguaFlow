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

func (s *EntEventStore) Replay(jobID int, afterSeq int64) []Event {
	ctx := context.Background()
	rows, err := s.client.SSEEvent.Query().
		Where(
			sseevent.JobIDEQ(jobID),
			sseevent.SeqGT(afterSeq),
		).
		Order(ent.Asc(sseevent.FieldSeq)).
		All(ctx)
	if err != nil {
		slog.Error("ent_event_store: replay failed", "job_id", jobID, "after_seq", afterSeq, "error", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
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
