package worker

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
	"sync"
)

// SetRootBudget supplies the Server's single root to every factory invocation.
func (r *PreviewRunner) SetRootBudget(root *resourcebudget.RootBudget, prepare func(context.Context, int) error) {
	r.factory.rootBudget, r.factory.prepareActor = root, prepare
}
func (r *RevisionPreviewRunner) SetRootBudget(root *resourcebudget.RootBudget, prepare func(context.Context, int) error) {
	r.factory.rootBudget, r.factory.prepareActor = root, prepare
}
func (r *QuickTranslateRunner) SetRootBudget(root *resourcebudget.RootBudget, prepare func(context.Context, int) error) {
	r.factory.rootBudget, r.factory.prepareActor = root, prepare
}
func (r *JobRunner) SetRootBudget(root *resourcebudget.RootBudget, prepare func(context.Context, int) error) {
	r.rootBudget, r.prepareActor = root, prepare
}
func (r *JobRunner) SetBudgetSource(source workstate.InventorySource, lock sync.Locker) {
	r.budgetSource, r.budgetInventoryLock = source, lock
}
