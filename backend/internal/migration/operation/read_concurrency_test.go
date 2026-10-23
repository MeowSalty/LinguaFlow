package operation

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestStatusReaderDoesNotBlockAtomicJournalPublication(t *testing.T) {
	fixture := newOperationFixture(t)
	dir := filepath.Join(fixture.root, "operations", "operation")
	j, err := Create(dir, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	// Holding the old descriptor must not prevent a durable replacement on
	// Windows; this reproduces the sharing failure without timing dependence.
	f, err := openRead(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	j.Phase = "converting"
	if err := SaveJournal(dir, j); err != nil {
		f.Close()
		t.Fatal("status read blocked atomic publication", err)
	}
	f.Close()
	done := make(chan struct{})
	var readers sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
					if _, _, err := Load(dir); err != nil {
						errors <- err
						return
					}
				}
			}
		}()
	}
	for i := range 40 {
		j.Phase = []string{"converting", "verifying"}[i%2]
		if err := SaveJournal(dir, j); err != nil {
			close(done)
			readers.Wait()
			t.Fatal(err)
		}
	}
	close(done)
	readers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal("concurrent status read failed", err)
	}
}
