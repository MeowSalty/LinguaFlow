package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type revokingStreamWriter struct {
	*safeRecorder
	once    sync.Once
	trigger string
	revoke  func()
}

func (w *revokingStreamWriter) Write(data []byte) (int, error) {
	n, err := w.safeRecorder.Write(data)
	if strings.Contains(string(data), w.trigger) {
		w.once.Do(w.revoke)
	}
	return n, err
}

func streamOrgFixture(t *testing.T) (*Server, *ent.Client, *ent.User, *ent.Job, *ent.OrgMembership) {
	t.Helper()
	s, client, u := jobStreamTestServer(t, 16, 10, 20)
	ctx := context.Background()
	org := client.Organization.Create().SetName("stream-org").SetSlug("stream-org").SaveX(ctx)
	membership := client.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(u.ID).SetRole(service.OrgRoleMember).SaveX(ctx)
	p := client.Project.Create().SetName("stream-org-project").SetOwnerOrgID(org.ID).SaveX(ctx)
	j := client.Job.Create().SetProjectID(p.ID).SetExecutionPlanID(1).SaveX(ctx)
	return s, client, u, j, membership
}

func TestHandlerJobStreamRevocationDuringReplay(t *testing.T) {
	s, client, u, j, membership := streamOrgFixture(t)
	ctx := context.Background()
	for seq := int64(1); seq <= 3; seq++ {
		client.SSEEvent.Create().SetJobID(j.ID).SetSeq(seq).SetType("translation").SetLevel("info").SetMessage("private").SaveX(ctx)
	}
	w := &revokingStreamWriter{safeRecorder: newSafeRecorder(), trigger: "id: 1", revoke: func() {
		if err := client.OrgMembership.DeleteOne(membership).Exec(ctx); err != nil {
			t.Error(err)
		}
	}}
	req := withAuthUser(httptest.NewRequest("GET", "/jobs/1/stream", nil), u)
	s.handleJobStreamWithInterval(w, req, j.ID, time.Millisecond)
	if ids := parseIDLines(w.String()); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("replayed after revocation: %v", ids)
	}
}

func TestHandlerJobStreamRevocationIdleAndLive(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "idle"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			s, client, u, j, membership := streamOrgFixture(t)
			// No replay store: a queued live event cannot be mistaken for history.
			s.eventBroker = event.NewBroker(nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			w := &revokingStreamWriter{safeRecorder: newSafeRecorder(), trigger: ": connected", revoke: func() {
				if err := client.OrgMembership.DeleteOne(membership).Exec(ctx); err != nil {
					t.Error(err)
				}
				if live {
					s.eventBroker.Publish(j.ID, event.Event{JobID: j.ID, Seq: 1, Type: "translation", Message: "private"})
				}
			}}
			req := withAuthUser(httptest.NewRequest("GET", "/jobs/1/stream", nil).WithContext(ctx), u)
			started := time.Now()
			s.handleJobStreamWithInterval(w, req, j.ID, 10*time.Millisecond)
			if time.Since(started) > 500*time.Millisecond {
				t.Fatal("stream did not close on scheduled authorization check")
			}
			if ids := parseIDLines(w.String()); len(ids) != 0 {
				t.Fatalf("data sent after revocation: %v", ids)
			}
			if strings.Contains(w.String(), ": keepalive") {
				t.Fatal("keepalive emitted after failed authorization")
			}
			reconnect := httptest.NewRecorder()
			s.handleJobStream(reconnect, withAuthUser(httptest.NewRequest("GET", "/jobs/1/stream?lastEventId=1", nil), u), j.ID)
			if reconnect.Code != 403 {
				t.Fatalf("reconnect status=%d body=%s", reconnect.Code, reconnect.Body.String())
			}
			if row := client.Job.GetX(context.Background(), j.ID); row.Status != "pending" {
				t.Fatalf("background job changed: %s", row.Status)
			}
		})
	}
}

type blockingStreamStore struct {
	event.EventStore
	blockLatest bool
	beforeBlock func()
	blocked     bool
}

func (s *blockingStreamStore) block(ctx context.Context) {
	s.blocked = true
	s.beforeBlock()
	<-ctx.Done()
}

func (s *blockingStreamStore) LatestSeq(ctx context.Context, _ int) (int64, bool, error) {
	if s.blockLatest {
		s.block(ctx)
	}
	return 0, false, ctx.Err()
}

func (s *blockingStreamStore) Replay(ctx context.Context, _ int, _ int64, _ int) ([]event.Event, error) {
	s.block(ctx)
	return nil, ctx.Err()
}

func TestHandlerJobStreamBlockedReplayClosesWithinRevocationDeadline(t *testing.T) {
	for _, phase := range []string{"latest", "replay"} {
		t.Run(phase, func(t *testing.T) {
			s, client, u, j, membership := streamOrgFixture(t)
			store := &blockingStreamStore{
				EventStore:  event.NewRingBufferStore(event.RingBufferConfig{Capacity: 8}),
				blockLatest: phase == "latest",
				beforeBlock: func() {
					if err := client.OrgMembership.DeleteOne(membership).Exec(context.Background()); err != nil {
						t.Error(err)
					}
				},
			}
			s.eventBroker = event.NewBroker(store)
			// The outer timeout is a test fail-safe, not the lookup/replay deadline.
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			req := withAuthUser(httptest.NewRequest("GET", "/jobs/1/stream", nil).WithContext(ctx), u)
			w := newSafeRecorder()
			started := time.Now()
			s.handleJobStream(w, req, j.ID)
			if !store.blocked {
				t.Fatal("blocking event store was not exercised")
			}
			if elapsed := time.Since(started); elapsed >= 10*time.Second || ctx.Err() != nil {
				t.Fatalf("%s prevented revocation: elapsed=%s request error=%v", phase, elapsed, ctx.Err())
			}
			if countIDLines(w.String()) != 0 || strings.Contains(w.String(), ": keepalive") {
				t.Fatalf("stream continued after blocked %s: %s", phase, w.String())
			}
		})
	}
}
