package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// ExecutionCapacityFailure carries only capacity coordinates. Body text,
// credentials and metadata do not belong in this diagnostic.
type ExecutionCapacityFailure struct {
	Stage, Dimension, Owner string
	Used, Requested, Limit  int64
	Drain                   bool
	Cause                   error
}

func (e *ExecutionCapacityFailure) Error() string {
	reason := "capacity rejected"
	if errors.Is(e.Cause, backend.ErrBuildCapacity) {
		reason = backend.ErrBuildCapacity.Error()
	} else if errors.Is(e.Cause, resourcebudget.ErrCapacity) {
		reason = resourcebudget.ErrCapacity.Error()
	}
	return fmt.Sprintf("execution capacity unavailable: stage=%s dimension=%s used=%d requested=%d limit=%d owner=%s drain=%t reason=%s", e.Stage, e.Dimension, e.Used, e.Requested, e.Limit, e.Owner, e.Drain, reason)
}
func (e *ExecutionCapacityFailure) Unwrap() error   { return e.Cause }
func (e *ExecutionCapacityFailure) Permanent() bool { return !e.Drain }

func executionCapacityFailure(err error, stage, owner string) error {
	if err == nil {
		return nil
	}
	var current *ExecutionCapacityFailure
	if errors.As(err, &current) {
		return err
	}
	var allocation *backend.AllocationCapacityError
	if errors.As(err, &allocation) {
		return &ExecutionCapacityFailure{Stage: stage, Dimension: "allocation_bytes", Owner: owner, Used: allocation.Used, Requested: allocation.Requested, Limit: allocation.Limit, Cause: err}
	}
	var ownership *backend.AllocationOwnershipError
	if errors.As(err, &ownership) {
		return &ExecutionCapacityFailure{Stage: stage, Dimension: "retained_allocation_bytes", Owner: owner, Used: ownership.Owned, Requested: ownership.Requested, Limit: ownership.Live, Cause: err}
	}
	var aggregate *resourcebudget.CapacityError
	if errors.As(err, &aggregate) {
		return &ExecutionCapacityFailure{Stage: stage, Dimension: aggregate.Dimension, Owner: owner, Used: aggregate.Used, Requested: aggregate.Requested, Limit: aggregate.Limit, Drain: aggregate.DrainOutside, Cause: err}
	}
	if errors.Is(err, backend.ErrBuildCapacity) {
		// Generic codec failures (depth, arithmetic, I/O envelopes) have no
		// allocation byte probe. Keep that distinction without inventing usage.
		return &ExecutionCapacityFailure{Stage: stage, Dimension: "build_bounds", Owner: owner, Cause: err}
	}
	return err
}

func logExecutionCapacityFailure(ctx context.Context, jobID, resourceID int, err error) {
	var detail *ExecutionCapacityFailure
	if !errors.As(err, &detail) {
		err = executionCapacityFailure(err, "job_execution", fmt.Sprintf("job:%d/resource:%d", jobID, resourceID))
		if !errors.As(err, &detail) {
			return
		}
	}
	slog.ErrorContext(ctx, "execution capacity rejected", "job_id", jobID, "job_resource_id", resourceID,
		"stage", detail.Stage, "dimension", detail.Dimension, "used", detail.Used, "requested", detail.Requested,
		"limit", detail.Limit, "owner", detail.Owner, "drain", detail.Drain)
}

func (s *JobService) publishExecutionFailureEvent(jobID int, eventType, message string, failure error) {
	var detail *ExecutionCapacityFailure
	if !errors.As(failure, &detail) {
		s.publishEvent(jobID, eventType, "error", "", message)
		return
	}
	if s.broker == nil {
		return
	}
	s.broker.Publish(jobID, event.Event{Type: eventType, JobID: jobID, Level: "error", Stage: detail.Stage,
		Message: message, CreatedAt: timeutil.NowUTC(), Metadata: map[string]any{
			"stage": detail.Stage, "dimension": detail.Dimension, "used": detail.Used, "requested": detail.Requested,
			"limit": detail.Limit, "owner": detail.Owner, "drain": detail.Drain,
		}})
}
