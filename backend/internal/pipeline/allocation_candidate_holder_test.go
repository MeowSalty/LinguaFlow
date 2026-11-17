package pipeline

import (
	"testing"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func TestAllocationCandidateHolderAndCompactState(t *testing.T) {
	b, err := backend.NewBuildBounds(DefaultCandidateLimits().ItemBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newCandidateChecked(Segment{}, 0, "translate", "text", "candidate", b)
	if err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().LiveBytes < int64(unsafe.Sizeof(Candidate{})) {
		t.Fatal("constructed candidate holder has no allocation owner")
	}
	state, err := PrepareAlignmentBounded(b, "translated", "text", nil, nil, ruby.ProtocolV2, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Alignment = state
	if err := c.SealAndCompact(b, DefaultCandidateLimits().ItemBytes); err != nil {
		t.Fatal(err)
	}
	if c.Alignment != state || c.Alignment.ProtocolVersion != ruby.ProtocolV2 || !c.HeaderOnly {
		t.Fatal("compaction replaced the owned state holder or lost scheduling identity")
	}
	payload := c.PreparedPayload
	frame := CandidateFrame{Payload: payload, ID: c.ID, Version: c.Version}
	d, err := backend.NewBuildBounds(DefaultCandidateLimits().ItemBytes, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCandidateFrame(d, frame)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != c.ID || decoded.Alignment == nil {
		t.Fatal("decoded candidate lost its identity or alignment")
	}
	if d.Snapshot().LiveBytes < int64(unsafe.Sizeof(Candidate{})) {
		t.Fatal("decoded candidate holder has no allocation owner")
	}
	decoded = nil
	frame = CandidateFrame{}
	payload = nil
	c = nil
	state = nil
	d.CloseRetired()
	b.CloseRetired()
}
