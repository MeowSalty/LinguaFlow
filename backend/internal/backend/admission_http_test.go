package backend_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestRequestAdmissionHTTPAcrossJobsResourcesAndRounds(t *testing.T) {
	for _, sameBackend := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_backend=%t", sameBackend), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			release := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			started := make(chan struct{}, 16)
			var mu sync.Mutex
			main := map[string]int{}
			align := map[int]int{}
			peakBackend := map[int]int{}
			activeBackend := map[int]int{}
			calls := 0
			var violation string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				job, _ := strconv.Atoi(q.Get("job"))
				backendID, _ := strconv.Atoi(q.Get("backend"))
				key := q.Get("job") + ":" + q.Get("round")
				alignment := q.Get("stage") == "alignment"
				mu.Lock()
				calls++
				activeBackend[backendID]++
				peakBackend[backendID] = max(peakBackend[backendID], activeBackend[backendID])
				if alignment {
					align[job]++
					if align[job] > 2 {
						violation = "Job alignment budget multiplied by resource or round"
					}
				} else {
					main[key]++
					if main[key] > 1 {
						violation = "round main budget multiplied by resource"
					}
				}
				mu.Unlock()
				started <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
				}
				mu.Lock()
				activeBackend[backendID]--
				if alignment {
					align[job]--
				} else {
					main[key]--
				}
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			pool := backend.NewLimiterPool()
			pool.Initialize(map[int]int{1: 6000, 2: 6000})
			defer pool.Shutdown()
			errs := make(chan error, 16)
			for job := range 2 {
				admission := newAdmission(t, pool, false, 16)
				for resource := range 2 {
					for round := range 2 {
						for _, stage := range []backend.RequestStage{backend.RequestStageMain, backend.RequestStageAlignment} {
							go func() {
								backendID := 1
								if !sameBackend && stage == backend.RequestStageAlignment {
									backendID = 2
								}
								permit, err := admission.Acquire(ctx, backend.RequestAdmissionIntent{ResourceID: resource, RoundIndex: round, Stage: stage, BackendID: backendID}, nil)
								if err != nil {
									errs <- err
									return
								}
								defer permit.Release()
								url := fmt.Sprintf("%s?job=%d&round=%d&stage=%s&backend=%d", server.URL, job, round, stage, backendID)
								req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
								if err != nil {
									errs <- err
									return
								}
								resp, err := server.Client().Do(req)
								if err == nil {
									_, err = io.Copy(io.Discard, resp.Body)
									resp.Body.Close()
								}
								errs <- err
							}()
						}
					}
				}
			}
			// Both Jobs simultaneously fill 2 round-main slots and their one
			// shared A=2. Holding all responses makes peaks deterministic.
			for range 8 {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("independent Job budgets did not fill HTTP capacity")
				}
			}
			releaseOnce.Do(func() { close(release) })
			for range 16 {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if violation != "" || calls != 16 {
				t.Fatalf("HTTP calls=%d violation=%s", calls, violation)
			}
			if sameBackend && peakBackend[1] != 8 {
				t.Fatalf("shared backend treated Job budgets as global capacity: %v", peakBackend)
			}
			if !sameBackend && (peakBackend[1] != 4 || peakBackend[2] != 4) {
				t.Fatalf("separate backend peaks=%v", peakBackend)
			}
		})
	}
}
