package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestExecutionCapacityDiagnosticPersistsSafeReason(t *testing.T) {
	cause := errors.Join(backend.ErrBuildCapacity, errors.New("private prompt /secret/path "+strings.Repeat("body", 10000)))
	err := executionCapacityFailure(cause, "input_build", "input:7")
	message := err.Error()
	if !errors.Is(err, backend.ErrBuildCapacity) || !strings.Contains(message, "stage=input_build dimension=build_bounds") || !strings.Contains(message, "reason="+backend.ErrBuildCapacity.Error()) {
		t.Fatalf("capacity reason/coordinates were lost: %s", message)
	}
	if strings.Contains(message, "private") || strings.Contains(message, "/secret") || strings.Contains(message, "body") || len(message) > 512 {
		t.Fatal("diagnostic exposed an uncontrolled cause")
	}
	var detail *ExecutionCapacityFailure
	if !errors.As(err, &detail) || detail.Cause != cause || !detail.Permanent() {
		t.Fatal("diagnostic lost the original typed cause")
	}
}

func TestExecutionCapacityDiagnosticRetainedAllocationCoordinates(t *testing.T) {
	cause := &backend.AllocationOwnershipError{Requested: 192, Live: 160, Owned: 64}
	err := executionCapacityFailure(errors.Join(cause, errors.New("private prompt")), "member_promotion", "candidate:7")
	var detail *ExecutionCapacityFailure
	if !errors.As(err, &detail) || detail.Dimension != "retained_allocation_bytes" || detail.Used != 64 || detail.Requested != 192 || detail.Limit != 160 || detail.Drain {
		t.Fatalf("retained allocation coordinates were lost: %v", err)
	}
	if !errors.Is(err, backend.ErrBuildCapacity) || !errors.Is(err, cause) || strings.Contains(err.Error(), "private") {
		t.Fatal("retained diagnostic lost its cause or exposed uncontrolled text")
	}
}
