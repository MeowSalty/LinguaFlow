package glossary

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestMemberMemoryFilePublicationConcurrentCloseAddSave(t *testing.T) {
	for _, medium := range []string{"memory", "file"} {
		for _, writer := range []string{"add", "save", "close", "rollback"} {
			if medium == "memory" && writer == "save" {
				continue
			}
			t.Run(medium+"/"+writer, func(t *testing.T) {
				g := NewMemory()
				if medium == "file" {
					var err error
					g, err = LoadFile(filepath.Join(t.TempDir(), "dictionary.csv"))
					if err != nil {
						t.Fatal(err)
					}
				}
				defer g.Close()
				if _, err := g.Add(t.Context(), Entry{Source: "SDK", Target: "规范"}); err != nil {
					t.Fatal(err)
				}
				root, err := resourcebudget.New(resourcebudget.Policy{})
				if err != nil {
					t.Fatal(err)
				}
				a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
				if err != nil {
					t.Fatal(err)
				}
				defer a.Close()
				if err = a.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "file-concurrency"}); err != nil {
					t.Fatal(err)
				}
				if err = g.BindAllocationOwner(t.Context(), a); err != nil {
					t.Fatal(err)
				}
				baseline := a.BuildBytes(g.allocationOwner)
				ctx := backend.WithBuildOwnership(t.Context(), a, "publication-attempt")
				ctx = backend.WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("publication-attempt", n) }, func(n int64) {
					if err := a.ReleaseBuildBytes("publication-attempt", n); err != nil {
						t.Error(err)
					}
				})
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
				fault := errors.New("publication rollback")
				var installCount atomic.Int32
				in := ResponseProcess{RequestID: "response", InputDigest: "digest", ItemLimit: 4096, BusinessLimit: 4096, Entries: []Entry{{Source: "Nebula", Target: "星云"}}, Publish: func(_ context.Context, _ ResponseBusiness, p *responsecontract.PreparedPublication) error {
					close(entered)
					<-release
					if writer == "rollback" {
						return fault
					}
					original := p.Install
					p.Install = func() { installCount.Add(1); original() }
					if err := p.InstallOnce(); err != nil {
						return err
					}
					return p.InstallOnce()
				}}
				processed := make(chan error, 1)
				go func() { out, e := g.ProcessResponse(ctx, in); out.Release(); processed <- e }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("publication barrier missing")
				}
				started, done := make(chan struct{}), make(chan error, 1)
				go func() {
					close(started)
					var e error
					switch writer {
					case "add", "rollback":
						_, e = g.Add(t.Context(), Entry{Source: "Atlas", Target: "地图"})
					case "save":
						e = g.Save(t.Context())
					case "close":
						e = g.Close()
					}
					done <- e
				}()
				<-started
				select {
				case e := <-done:
					t.Fatalf("writer passed unfinished publication: %v", e)
				case <-time.After(20 * time.Millisecond):
				}
				releaseOnce.Do(func() { close(release) })
				select {
				case e := <-processed:
					if writer == "rollback" {
						if !errors.Is(e, fault) {
							t.Fatal(e)
						}
					} else if e != nil {
						t.Fatal(e)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("publication deadlocked")
				}
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("writer deadlocked")
				}
				if writer == "rollback" {
					if installCount.Load() != 0 || g.Len() != 2 {
						t.Fatalf("rollback published private dictionary: installed=%d count=%d", installCount.Load(), g.Len())
					}
					if a.BuildBytes(g.allocationOwner) <= baseline {
						t.Fatal("concurrent Add acquired no actual dictionary backing")
					}
				} else if installCount.Load() != 1 {
					t.Fatalf("installation count=%d", installCount.Load())
				}
				if medium == "file" && writer != "close" {
					if err = g.Save(t.Context()); err != nil {
						t.Fatal(err)
					}
					reloaded, e := LoadFile(g.Path())
					if e != nil {
						t.Fatal(e)
					}
					if reloaded.Len() != g.Len() {
						t.Fatal("Save/reload lost serialized writer")
					}
					reloaded.Close()
				}
				if err = g.Close(); err != nil {
					t.Fatal(err)
				}
				if got := root.Snapshot(1).Instance; got != (resourcebudget.Vector{}) {
					t.Fatalf("last dictionary/receipt reference leaked: %+v owners=%v", got, a.BuildAllocationSnapshot())
				}
			})
		}
	}
}
