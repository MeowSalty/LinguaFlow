package workstate

import (
	"context"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"testing"
)

func fixturePromotion(in MemberPromotion) MemberPromotion {
	in.Outcome.Key, in.Outcome.Accepted = in.Key, in.Candidate != nil
	if in.Candidate == nil && in.Outcome.Reason == "" {
		in.Outcome.Reason = responsecontract.MemberInvalid
	}
	return in
}

func requestMemberKey(r Request, ordinal int) MemberKey {
	return MemberKey{RequestID: r.ID, Ordinal: ordinal, CaptureEpoch: r.Scope.RetryEpoch}
}

// publishFixtureBusiness leaves the independently hashed output available until
// promotion. Capture and archive are separate boundaries so ACK fixtures can
// target each fact without retaining an obsolete whole response body.
func publishFixtureBusiness(t *testing.T, ctx context.Context, store *Store, r Request) {
	t.Helper()
	payload := []byte(`{"target":"translated"}`)
	digest := payloadDigest(payload)
	key := requestMemberKey(r, 0)
	if _, err := store.PublishOutputs(ctx, OutputPublication{Key: key, Members: []OutputMember{{Key: key, Payload: payload, Bytes: int64(len(payload)), Digest: digest}}, Next: 1, Digest: payloadDigest([]byte(digest)), Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishMemberBusiness(ctx, r.ID, nil); err != nil {
		t.Fatal(err)
	}
}
