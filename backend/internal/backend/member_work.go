package backend

import (
	"context"
	"sync"
)

// MemberWorkEvent records actual data presented to readers, hashers and
// decoders. It retains no payload, request context or execution owner.
type MemberWorkEvent struct {
	Phase, Operation string
	Bytes, Rows      int64
	DurationNS       int64 // Monotonic elapsed time, only for duration events.
	// Pure duration observations carry zero Bytes/Rows: data processing is
	// already recorded by its real reader, decoder or mutation event.
	FullJob bool
}
type memberWorkKey struct{}

func WithMemberWorkRecorder(ctx context.Context, observe func(MemberWorkEvent)) context.Context {
	return context.WithValue(ctx, memberWorkKey{}, observe)
}
func MemberWorkRecorder(ctx context.Context) func(MemberWorkEvent) {
	if ctx == nil {
		return nil
	}
	observe, _ := ctx.Value(memberWorkKey{}).(func(MemberWorkEvent))
	return observe
}
func ObserveMemberWork(ctx context.Context, event MemberWorkEvent) {
	if ctx == nil {
		return
	}
	if observe, ok := ctx.Value(memberWorkKey{}).(func(MemberWorkEvent)); ok && observe != nil {
		observe(event)
	}
}

func TrackMemberFrame(ctx context.Context, bytes int64, release func()) func() {
	ObserveMemberWork(ctx, MemberWorkEvent{Phase: "member", Operation: "frame_acquire", Bytes: bytes, Rows: 1})
	var once sync.Once
	return func() {
		once.Do(func() {
			if release != nil {
				release()
			}
			ObserveMemberWork(ctx, MemberWorkEvent{Phase: "member", Operation: "frame_release", Bytes: bytes, Rows: 1})
		})
	}
}
