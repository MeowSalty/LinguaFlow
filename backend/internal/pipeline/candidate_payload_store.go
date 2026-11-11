package pipeline

import (
	"context"
	"fmt"
	"sort"
)

func (s *MemoryRoundStore) CandidateHeaders(_ context.Context, after, limit int) ([]*Candidate, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Candidate
	for id, h := range s.headers {
		if _, active := s.candidates[id]; active && h.Index+1 > after {
			out = append(out, h.Candidate())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	if len(out) > limit {
		out = out[:limit]
	}
	if len(out) > 0 {
		after = out[len(out)-1].Index + 1
	}
	return out, after, nil
}
func (s *MemoryRoundStore) Candidate(ctx context.Context, id string, maxBytes int64) (*Candidate, error) {
	s.mu.Lock()
	payload, ok := s.candidates[id]
	if !ok || int64(len(payload)) > maxBytes {
		s.mu.Unlock()
		return nil, fmt.Errorf("candidate payload is unavailable or exceeds hard limit")
	}
	data := append([]byte(nil), payload...)
	s.mu.Unlock()
	var c *Candidate
	decode := func() error {
		var err error
		c, err = DecodeCandidateBounded(data, maxBytes, 0)
		if err == nil {
			c.StoredBytes = int64(len(data))
		}
		return err
	}
	var err error
	if r := ExecutionRuntimeFromContext(ctx); r != nil {
		err = r.Run(ctx, decode)
	} else {
		err = decode()
	}
	return c, err
}

func (s *MemoryRoundStore) CandidateFrame(_ context.Context, id string, maxBytes int64) (CandidateFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, ok := s.candidates[id]
	h := s.headers[id]
	if !ok || int64(len(payload)) > maxBytes {
		return CandidateFrame{}, ErrCandidateCapacity
	}
	// Canonical bytes are immutable and a live candidate has one consumer. A
	// later map replacement never mutates this backing, so no clone is needed.
	return CandidateFrame{Payload: payload, ID: id, Version: h.Version, Index: h.Index, WorkID: h.WorkID}, nil
}
