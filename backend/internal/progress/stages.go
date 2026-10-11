package progress

import (
	"context"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// WorkStateObserver refreshes stage diagnostics after a durable handoff, save or
// confirmation. It must be notified outside the persistence transaction.
type WorkStateObserver interface{ OnWorkStateChange() }

func NotifyWorkState(reporter Reporter) {
	if observer, ok := reporter.(WorkStateObserver); ok {
		observer.OnWorkStateChange()
	}
}

func (r *DBReporter) OnWorkStateChange() {
	if !r.beginWrite() {
		return
	}
	defer r.sourceMu.RUnlock()
	if r.broker == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	counts, err := workstate.ReadStageCounts(ctx, r.client, r.jobID)
	if err != nil {
		return
	}
	r.broker.Publish(r.jobID, event.Event{Type: "stage_counts", JobID: r.jobID, Level: "info", Message: "任务阶段进度", Metadata: map[string]any{"stages": counts}, CreatedAt: timeutil.NowUTC()})
}
