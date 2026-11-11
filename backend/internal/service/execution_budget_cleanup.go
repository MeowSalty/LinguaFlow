package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// ExecutionBudgetCleanup shares the server's root and SQL inventory barrier.
// FreezeRegistration must fence queue registration while allowing workers to
// finish their existing claims. Cancellation includes waiting queue entries.
type ExecutionBudgetCleanup struct {
	Root               *resourcebudget.RootBudget
	Source             workstate.InventorySource
	InventoryMu        *sync.RWMutex
	Lifecycle          *tasklife.Coordinator
	FreezeRegistration func(context.Context) (func(), error)
	CancelTranslation  func(int)
}

func (s *ProjectService) SetExecutionBudgetCleanup(cleanup *ExecutionBudgetCleanup) {
	s.budgetCleanup = cleanup
}

type cleanupInventory struct {
	total, orphan resourcebudget.Vector
	actors        map[int]resourcebudget.Vector
}

func addCleanupVector(a, b resourcebudget.Vector) (resourcebudget.Vector, error) {
	for _, pair := range [][2]int64{{a.UnconfirmedMembers, b.UnconfirmedMembers}, {a.PayloadBytes, b.PayloadBytes}, {a.LogicalStorageBytes, b.LogicalStorageBytes}} {
		if pair[1] < 0 || pair[0] > math.MaxInt64-pair[1] {
			return resourcebudget.Vector{}, resourcebudget.ErrInvalid
		}
	}
	return resourcebudget.Vector{UnconfirmedMembers: a.UnconfirmedMembers + b.UnconfirmedMembers, PayloadBytes: a.PayloadBytes + b.PayloadBytes, LogicalStorageBytes: a.LogicalStorageBytes + b.LogicalStorageBytes}, nil
}

func subtractCleanupVector(a, b resourcebudget.Vector) (resourcebudget.Vector, error) {
	v := resourcebudget.Vector{UnconfirmedMembers: a.UnconfirmedMembers - b.UnconfirmedMembers, PayloadBytes: a.PayloadBytes - b.PayloadBytes, LogicalStorageBytes: a.LogicalStorageBytes - b.LogicalStorageBytes}
	return v, v.Validate()
}

// projectInventory uses fixed-size ID pages and three SQL scalar roots per job.
// The actor map contains only the already bounded root cache, including zeros.
func (c *ExecutionBudgetCleanup) projectInventory(ctx context.Context, client *ent.Client, projectID int, actors []int) (cleanupInventory, error) {
	stock := cleanupInventory{actors: make(map[int]resourcebudget.Vector, len(actors))}
	for _, actor := range actors {
		stock.actors[actor] = resourcebudget.Vector{}
	}
	for after := 0; ; {
		ids, err := client.Job.Query().Where(job.ProjectIDEQ(projectID), job.IDGT(after)).Order(ent.Asc(job.FieldID)).Limit(16).IDs(ctx)
		if err != nil {
			return stock, err
		}
		if len(ids) == 0 {
			return stock, nil
		}
		for _, id := range ids {
			actor, err := c.Source.Actor(ctx, id)
			if err != nil {
				return stock, err
			}
			v, storage, err := c.Source.Job(ctx, id)
			if err != nil {
				return stock, err
			}
			v.LogicalStorageBytes = storage
			stock.total, err = addCleanupVector(stock.total, v)
			if err != nil {
				return stock, err
			}
			if actor == 0 {
				stock.orphan, err = addCleanupVector(stock.orphan, v)
			} else if old, ok := stock.actors[actor]; ok {
				stock.actors[actor], err = addCleanupVector(old, v)
			}
			if err != nil {
				return stock, err
			}
			after = id
		}
	}
}

func (c *ExecutionBudgetCleanup) affectedJobs(ctx context.Context, client *ent.Client, projectID, resourceID int, visit func(int) error) error {
	for after := 0; ; {
		q := client.Job.Query().Where(job.ProjectIDEQ(projectID), job.IDGT(after)).Order(ent.Asc(job.FieldID)).Limit(16)
		if resourceID > 0 {
			q.Where(job.HasJobResourcesWith(jobresource.HasResourceWith(resource.IDEQ(resourceID))))
		}
		ids, err := q.IDs(ctx)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err = visit(id); err != nil {
				return err
			}
			after = id
		}
	}
}

func (c *ExecutionBudgetCleanup) prepare(ctx context.Context, client *ent.Client, projectID, resourceID int, deleting bool) (func(context.Context) error, error) {
	if c == nil {
		return func(context.Context) error { return nil }, nil
	}
	if c.Root == nil || c.InventoryMu == nil || c.Source.DB == nil {
		return nil, fmt.Errorf("execution cleanup is not configured")
	}
	unfreeze := func() {}
	if c.FreezeRegistration != nil {
		var err error
		unfreeze, err = c.FreezeRegistration(ctx)
		if err != nil {
			return nil, err
		}
	}
	prepared := false
	defer func() {
		if !prepared {
			unfreeze()
		}
	}()
	joinCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.affectedJobs(joinCtx, client, projectID, resourceID, func(id int) error {
		if deleting && c.CancelTranslation != nil {
			c.CancelTranslation(id)
		}
		if !deleting && c.Lifecycle.Busy("translation", id) {
			return tasklife.ErrBusy
		}
		for c.Lifecycle.Busy("translation", id) {
			if c.CancelTranslation != nil {
				c.CancelTranslation(id)
			}
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-joinCtx.Done():
				timer.Stop()
				return joinCtx.Err()
			case <-timer.C:
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Existing writers in other projects may continue; the exclusive barrier
	// gives both snapshots the same durable facts outside this SQL mutation.
	c.InventoryMu.Lock()
	actors := c.Root.CachedActorIDs()
	before, err := c.projectInventory(ctx, client, projectID, actors)
	if err != nil {
		c.InventoryMu.Unlock()
		return nil, err
	}
	prepared = true
	var once sync.Once
	var finishErr error
	return func(ctx context.Context) error {
		once.Do(func() {
			defer unfreeze()
			defer c.InventoryMu.Unlock()
			factCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			after, err := c.projectInventory(factCtx, client, projectID, actors)
			if err != nil {
				finishErr = err
				return
			}
			total, err := subtractCleanupVector(before.total, after.total)
			if err != nil {
				finishErr = err
				return
			}
			orphan, err := subtractCleanupVector(before.orphan, after.orphan)
			if err != nil {
				finishErr = err
				return
			}
			deltas := make(map[int]resourcebudget.Vector, len(actors))
			for _, actor := range actors {
				delta, err := subtractCleanupVector(before.actors[actor], after.actors[actor])
				if err != nil {
					finishErr = err
					return
				}
				deltas[actor] = delta
			}
			finishErr = c.Root.ReleaseInventoryTotals(total, orphan, deltas)
		})
		return finishErr
	}, nil
}

func finishExecutionCleanup(ctx context.Context, finish func(context.Context) error, result *error) {
	*result = errors.Join(*result, finish(ctx))
}
