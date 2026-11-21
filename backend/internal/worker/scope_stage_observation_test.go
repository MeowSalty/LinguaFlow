package worker

import (
	"encoding/json"
	"math/bits"
	"sort"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

// A fixed histogram records bounded scalar timings; it never retains SQL,
// documents, per-member identity, payloads or a list of every sample.
type scopeStageTiming struct {
	Count      uint64
	Total, Max time.Duration
	Buckets    [36]uint64
}
type scopeStageObserver struct {
	mu     sync.Mutex
	phases map[string]scopeStageTiming
}

func (o *scopeStageObserver) duration(key string, elapsed time.Duration) {
	if elapsed < 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.phases == nil {
		o.phases = make(map[string]scopeStageTiming)
	}
	row := o.phases[key]
	row.Count++
	row.Total += elapsed
	row.Max = max(row.Max, elapsed)
	bucket := min(len(row.Buckets)-1, bits.Len64(uint64(elapsed)))
	row.Buckets[bucket]++
	o.phases[key] = row
}
func (o *scopeStageObserver) Observe(event persistence.Observation) {
	o.duration(event.Metadata.Operation+"/"+event.Phase, event.Duration)
}
func (o *scopeStageObserver) snapshot() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	type measurement struct {
		Phase                                  string
		Count                                  uint64
		TotalNS, MaxNS, P50UpperNS, P95UpperNS int64
	}
	out := make([]measurement, 0, len(o.phases))
	for key, row := range o.phases {
		percentile := func(percent uint64) int64 {
			target, seen := (row.Count*percent+99)/100, uint64(0)
			for bucket, count := range row.Buckets {
				seen += count
				if seen >= target {
					return int64(1) << bucket
				}
			}
			return 0
		}
		out = append(out, measurement{key, row.Count, int64(row.Total), int64(row.Max), percentile(50), percentile(95)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Phase < out[j].Phase })
	data, _ := json.Marshal(out)
	return string(data)
}
