package persistence

import (
	"testing"
	"time"
)

func TestCollectorSeparatesPolicySQLDuration(t *testing.T) {
	collector := &Collector{}
	for _, observation := range []Observation{
		{Phase: "driver_exec", Duration: 7 * time.Millisecond},
		{Phase: "driver_policy_query", Duration: 2 * time.Millisecond},
		{Phase: "driver_policy_exec", Duration: 3 * time.Millisecond},
		{Phase: "driver_transaction", Duration: 11 * time.Millisecond},
		{Phase: "driver_rows", Duration: 13 * time.Millisecond},
	} {
		collector.Observe(observation)
	}
	snapshot := collector.Snapshot()
	if snapshot.DriverCalls != 1 || snapshot.DriverDuration != 7*time.Millisecond {
		t.Fatalf("policy SQL changed application SQL accounting: %+v", snapshot)
	}
	if snapshot.PolicyCalls != 2 || snapshot.PolicyDuration != 5*time.Millisecond {
		t.Fatalf("policy query/exec duration not accumulated: %+v", snapshot)
	}
	if snapshot.DriverTransactions != 1 || snapshot.DriverTransactionDuration != 11*time.Millisecond || snapshot.RowsDuration != 13*time.Millisecond {
		t.Fatalf("transaction/rows duration mixed into policy accounting: %+v", snapshot)
	}
}
