package workstate

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

// ResponseIdentityProof contains only immutable SQL identity scalars. It can
// outlive an uncertain write without retaining a response or an attempt context.
type ResponseIdentityProof struct {
	DatabaseID, JobID, ResourceID, RoundID, MemberCount int
	RequestID, SnapshotDigest, FreezeDigest             string
	CaptureEpoch                                        int64
}

func ResponseIdentityFromRow(r *ent.WorkRequest) ResponseIdentityProof {
	return ResponseIdentityProof{DatabaseID: r.ID, JobID: r.JobID, ResourceID: r.ResourceID, RoundID: r.JobRoundID, MemberCount: r.PromotionCount, RequestID: r.Identity, SnapshotDigest: r.SnapshotDigest, FreezeDigest: r.FreezeDigest, CaptureEpoch: r.RetryEpoch}
}

func (p ResponseIdentityProof) Matches(r *ent.WorkRequest) bool {
	return r.ID == p.DatabaseID && r.Identity == p.RequestID && r.JobID == p.JobID && r.ResourceID == p.ResourceID && r.JobRoundID == p.RoundID && r.PromotionCount == p.MemberCount && r.RetryEpoch == p.CaptureEpoch && r.ResponseFormatVersion == execution.ResponseFormatVersion && r.SnapshotDigest == p.SnapshotDigest && r.FreezeDigest == p.FreezeDigest
}

// WithCommitConfirmation gates storage refunds on the same bounded identity
// proof used to acknowledge the write. The callback must capture only scalars.
// Verification owns the SQL job lock and excludes the active storage budget.
func WithCommitConfirmation(ctx context.Context, client *ent.Client, jobID int, check func(context.Context, *ent.Client) (bool, error)) context.Context {
	return context.WithValue(ctx, storageConfirmationKey{}, func(factCtx context.Context) (bool, error) {
		return VerifyCommitConfirmation(factCtx, client, jobID, check)
	})
}

// VerifyCommitConfirmation also protects non-storage retirement, such as an
// unpublished dictionary cache. Absence is authoritative only after Job's SQL
// lock joins the original writer; this read never reenters its storage budget.
func VerifyCommitConfirmation(ctx context.Context, client *ent.Client, jobID int, check func(context.Context, *ent.Client) (bool, error)) (bool, error) {
	return NewStore(client).verifyLocked(ctx, jobID, check)
}
