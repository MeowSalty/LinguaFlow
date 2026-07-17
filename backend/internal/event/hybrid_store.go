package event

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sseevent"
)

type HybridStore struct {
	ringStore *RingBufferStore
	entStore  *EntEventStore
	nextSeq   atomic.Int64
}

func NewHybridStore(ringStore *RingBufferStore, entStore *EntEventStore) (*HybridStore, error) {
	hs := &HybridStore{
		ringStore: ringStore,
		entStore:  entStore,
	}
	if err := hs.initSeqFromDB(); err != nil {
		return nil, fmt.Errorf("hybrid store init: %w", err)
	}
	return hs, nil
}

func (s *HybridStore) initSeqFromDB() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	count, err := s.entStore.client.SSEEvent.Query().Count(ctx)
	if err != nil {
		return fmt.Errorf("query event count: %w", err)
	}
	if count == 0 {
		slog.Info("hybrid_store: no existing events in DB, starting from 0")
		return nil
	}

	maxSeq, err := s.entStore.client.SSEEvent.Query().
		Aggregate(ent.Max(sseevent.FieldSeq)).
		Int(ctx)
	if err != nil {
		return fmt.Errorf("query max seq from DB: %w", err)
	}
	s.nextSeq.Store(int64(maxSeq))
	slog.Info("hybrid_store: initialized seq from DB", "max_seq", maxSeq)
	return nil
}

func (s *HybridStore) Append(jobID int, evt Event) (int64, error) {
	seq := s.nextSeq.Add(1)
	evt.Seq = seq

	_, dbErr := s.entStore.Append(jobID, evt)
	if dbErr != nil {
		slog.Warn("hybrid_store: DB append failed, degrading to memory-only",
			"job_id", jobID, "seq", seq, "error", dbErr)
	}

	s.ringStore.AppendWithSeq(jobID, evt)

	return seq, dbErr
}

func (s *HybridStore) Replay(jobID int, afterSeq int64) []Event {
	events := s.ringStore.Replay(jobID, afterSeq)
	if events != nil && len(events) > 0 {
		return events
	}
	return s.entStore.Replay(jobID, afterSeq)
}

func (s *HybridStore) Purge(jobID int) {
	s.ringStore.Purge(jobID)
}
