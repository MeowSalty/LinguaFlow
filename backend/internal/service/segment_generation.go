package service

import (
	"context"
	"errors"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
)

var (
	ErrSourceReadOnly         = errors.New("file source text is read-only")
	ErrSourceRevisionConflict = errors.New("source revision changed")
)

// GuardSourceGeneration 在调用方的事务中锁定资源版本。
// 调用方必须传入事务客户端，并把所有依赖写入都放在该事务内。
func GuardSourceGeneration(ctx context.Context, txClient *ent.Client, resourceID int, expected int64) error {
	n, err := txClient.Resource.Update().Where(resource.IDEQ(resourceID), resource.SourceGenerationEQ(expected)).SetSourceGeneration(expected).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSourceRevisionConflict
	}
	return nil
}

// AdvanceTranslationGeneration 守护源版本并使输出快照失效。
// 结果写入与本次更新必须使用同一个事务客户端。
func AdvanceTranslationGeneration(ctx context.Context, txClient *ent.Client, resourceID int, expected int64) error {
	n, err := txClient.Resource.Update().Where(resource.IDEQ(resourceID), resource.SourceGenerationEQ(expected)).AddTranslationGeneration(1).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSourceRevisionConflict
	}
	return nil
}

// WithResourceTranslation 把结果写入与代数递增作为一个整体提交。
func WithResourceTranslation(ctx context.Context, client *ent.Client, resourceID int, expected int64, write func(*ent.Client) error) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := AdvanceTranslationGeneration(ctx, tx.Client(), resourceID, expected); err != nil {
		return err
	}
	if err := write(tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}

// ValidateJobResourceSource 拒绝针对其他源修订版本受理的工作。
func ValidateJobResourceSource(ctx context.Context, client *ent.Client, item *ent.JobResource) error {
	row, err := client.Resource.Query().Where(resource.HasJobResourcesWith(jobresource.IDEQ(item.ID))).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrSourceRevisionConflict
	}
	if err != nil {
		return err
	}
	if row.SourceGeneration != item.SourceGeneration || !ptrIntEq(row.CurrentSourceRevisionID, item.SourceRevisionID) {
		return ErrSourceRevisionConflict
	}
	return nil
}

func guardJobResourceSource(ctx context.Context, client *ent.Client, item *ent.JobResource) error {
	row, err := client.Resource.Query().Where(resource.HasJobResourcesWith(jobresource.IDEQ(item.ID))).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrSourceRevisionConflict
	}
	if err != nil {
		return err
	}
	if !ptrIntEq(row.CurrentSourceRevisionID, item.SourceRevisionID) {
		return ErrSourceRevisionConflict
	}
	return GuardSourceGeneration(ctx, client, row.ID, item.SourceGeneration)
}
