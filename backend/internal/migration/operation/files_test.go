package operation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestDecodeRejectsAmbiguousPersistentDocuments(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "duplicate keys", data: `{"name":"first","name":"second"}`},
		{name: "unknown fields", data: `{"name":"value","unexpected":true}`},
		{name: "trailing data", data: `{"name":"value"} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var document struct {
				Name string `json:"name"`
			}
			if err := Decode([]byte(test.data), &document); err == nil {
				t.Fatal("Decode accepted an ambiguous persistent document")
			}
		})
	}
}

func TestCreateLoadAndAtomicJournalUpdatesKeepPlanImmutable(t *testing.T) {
	fixture := newOperationFixture(t)
	directory := filepath.Join(fixture.root, "operations", "operation")
	if _, err := Create(directory, fixture.plan); err != nil {
		t.Fatalf("Create: %v", err)
	}
	planPath := filepath.Join(directory, "plan.json")
	planBefore, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(directory, "plan.json"), fixture.plan, true); err == nil {
		t.Fatal("private create replaced an existing immutable plan")
	}

	loadedPlan, journal, err := Load(directory)
	if err != nil {
		t.Fatalf("initial Load: %v", err)
	}
	if Digest(loadedPlan) != Digest(fixture.plan) || journal.Phase != "planned" {
		t.Fatalf("initial round trip mismatch: plan=%+v journal=%+v", loadedPlan, journal)
	}

	phases := []string{"staging", "converting", "verifying", "ready"}
	for i := 0; i < 32; i++ {
		journal.Phase = phases[i%len(phases)]
		if err := SaveJournal(directory, journal); err != nil {
			t.Fatalf("SaveJournal update %d: %v", i, err)
		}
		currentPlan, currentJournal, err := Load(directory)
		if err != nil {
			t.Fatalf("Load after journal update %d: %v", i, err)
		}
		if Digest(currentPlan) != Digest(fixture.plan) || currentJournal.Phase != phases[i%len(phases)] {
			t.Fatalf("round trip after journal update %d: plan digest changed or phase=%q", i, currentJournal.Phase)
		}
	}

	planAfter, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(planBefore, planAfter) {
		t.Fatal("journal updates changed the immutable plan bytes")
	}
	updatedPlan, updatedJournal, err := Load(directory)
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if Digest(updatedPlan) != Digest(fixture.plan) || updatedJournal.Phase != "ready" {
		t.Fatalf("final round trip mismatch: plan=%+v journal=%+v", updatedPlan, updatedJournal)
	}
}

func TestOperationLockSerializesWritersAndCanBeReacquired(t *testing.T) {
	fixture := newOperationFixture(t)
	directory := filepath.Join(fixture.root, "operations", "operation")
	if _, err := Create(directory, fixture.plan); err != nil {
		t.Fatal(err)
	}

	firstUnlock, err := Lock(directory)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	if _, err := Lock(directory); errorCode(err) != NeedsAction {
		t.Fatalf("competing Lock error = %v, want %q", err, NeedsAction)
	}
	if err := firstUnlock(); err != nil {
		t.Fatalf("first unlock: %v", err)
	}
	secondUnlock, err := Lock(directory)
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	if err := secondUnlock(); err != nil {
		t.Fatalf("second unlock: %v", err)
	}
}

func TestPublishDirectoryPreservesExistingTarget(t *testing.T) {
	fixture := newPublishFixture(t)
	if err := os.Mkdir(fixture.target, 0700); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(fixture.target, "sentinel")
	sentinel := []byte("pre-existing target bytes")
	if err := os.WriteFile(sentinelPath, sentinel, 0600); err != nil {
		t.Fatal(err)
	}

	err := PublishDirectory(fixture.staging, fixture.target, fixture.stagingID, fixture.parentID)
	if errorCode(err) != TargetNotEmpty {
		t.Fatalf("PublishDirectory error = %v, want %q", err, TargetNotEmpty)
	}
	got, err := os.ReadFile(sentinelPath)
	if err != nil || !bytes.Equal(got, sentinel) {
		t.Fatalf("existing target sentinel changed: bytes=%q error=%v", got, err)
	}
	if _, err := os.Stat(fixture.staging); err != nil {
		t.Fatalf("staging directory was not preserved: %v", err)
	}
}

func TestPublishDirectoryRejectsReplacedSourceOrParentIdentity(t *testing.T) {
	tests := []struct {
		name    string
		replace func(*testing.T, *publishFixture)
	}{
		{
			name: "staging source",
			replace: func(t *testing.T, fixture *publishFixture) {
				renamed := fixture.staging + "-original"
				if err := os.Rename(fixture.staging, renamed); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(fixture.staging, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.staging, "replacement"), []byte("new source"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "target parent",
			replace: func(t *testing.T, fixture *publishFixture) {
				renamed := fixture.parent + "-original"
				if err := os.Rename(fixture.parent, renamed); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(fixture.parent, 0700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublishFixture(t)
			test.replace(t, fixture)
			err := PublishDirectory(fixture.staging, fixture.target, fixture.stagingID, fixture.parentID)
			if errorCode(err) != BaselineChanged {
				t.Fatalf("PublishDirectory error = %v, want %q", err, BaselineChanged)
			}
			if _, err := os.Lstat(fixture.target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target appeared after rejected publication: %v", err)
			}
			if _, err := os.Stat(fixture.staging); err != nil {
				t.Fatalf("staging source was not preserved: %v", err)
			}
		})
	}
}

func TestValidatePathRejectsSymlinkReparsePoint(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "ordinary")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		var linkErr *os.LinkError
		if runtime.GOOS == "windows" && errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.Errno(1314)) {
			t.Skipf("Windows denied symlink creation because the process lacks symlink privilege: %v", err)
		}
		t.Fatalf("create symlink reparse point: %v", err)
	}
	if err := ValidatePath(link, true); errorCode(err) != Incompatible {
		t.Fatalf("ValidatePath error = %v, want %q", err, Incompatible)
	}
}

func TestHashAndCopyPreserveExistingDestinationAndDetachSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.bin")
	original := []byte("source bytes stay independent")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}

	hash, err := Hash(context.Background(), source)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	expectedHash := sha256.Sum256(original)
	if hash.Size != int64(len(original)) || hash.SHA256 != hex.EncodeToString(expectedHash[:]) {
		t.Fatalf("Hash returned unexpected artifact: %+v", hash)
	}

	existing := filepath.Join(root, "existing-copy.bin")
	sentinel := []byte("destination must survive")
	if err := os.WriteFile(existing, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(context.Background(), source, existing); err == nil {
		t.Fatal("Copy replaced an existing destination")
	}
	got, err := os.ReadFile(existing)
	if err != nil || !bytes.Equal(got, sentinel) {
		t.Fatalf("existing destination changed: bytes=%q error=%v", got, err)
	}
	got, err = os.ReadFile(source)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("failed copy changed source: bytes=%q error=%v", got, err)
	}

	copyPath := filepath.Join(root, "detached-copy.bin")
	if _, err := Copy(context.Background(), source, copyPath); err != nil {
		t.Fatalf("Copy to absent destination: %v", err)
	}
	if err := os.WriteFile(source, []byte("later source change"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(copyPath)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("copy follows later source changes: bytes=%q error=%v", got, err)
	}
}

type operationFixture struct {
	root string
	plan *Plan
}

func newOperationFixture(t *testing.T) operationFixture {
	t.Helper()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.Mkdir(sourceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	sourceDB := filepath.Join(sourceRoot, "source.sqlite")
	if err := os.WriteFile(sourceDB, []byte("source database fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	targetParent := filepath.Join(root, "target-parent")
	if err := os.Mkdir(targetParent, 0700); err != nil {
		t.Fatal(err)
	}
	operationParent := filepath.Join(root, "operations")
	if err := os.Mkdir(operationParent, 0700); err != nil {
		t.Fatal(err)
	}
	sourceIdentity, err := Identity(sourceDB)
	if err != nil {
		t.Fatal(err)
	}
	parentIdentity, err := Identity(targetParent)
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	return operationFixture{
		root: root,
		plan: &Plan{
			Version:           Version,
			Strategy:          Clone,
			DatabaseBackup:    BackupAuto,
			ID:                id,
			Kind:              "upgrade",
			Route:             "upgrade-v015-auth1",
			Build:             "contract-test-build",
			CreatedAt:         time.Now().UTC().Truncate(time.Second),
			Source:            Endpoint{DataDir: sourceRoot, Driver: "sqlite", Mode: "serve", Identity: sourceIdentity, InputDigest: digestHex("source input"), DatabaseDSN: "file:" + filepath.ToSlash(sourceDB) + "?mode=ro"},
			SourceFingerprint: digestHex("source fingerprint"),
			SourceSchema:      digestHex("source schema"),
			ObjectsDigest:     digestHex("objects baseline"),
			TargetSignature:   digestHex("target signature"),
			Target:            Target{DataDir: filepath.Join(targetParent, "target"), ParentIdentity: parentIdentity},
			Accounts:          []Account{{UserID: 1, Username: "contract-user"}},
		},
	}
}

type publishFixture struct {
	staging   string
	target    string
	parent    string
	stagingID string
	parentID  string
}

func newPublishFixture(t *testing.T) *publishFixture {
	t.Helper()
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	parent := filepath.Join(root, "target-parent")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "payload"), []byte("staged bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	stagingID, err := Identity(staging)
	if err != nil {
		t.Fatal(err)
	}
	parentID, err := Identity(parent)
	if err != nil {
		t.Fatal(err)
	}
	return &publishFixture{staging: staging, target: filepath.Join(parent, "published"), parent: parent, stagingID: stagingID, parentID: parentID}
}

func digestHex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func errorCode(err error) string {
	var operationErr *Error
	if errors.As(err, &operationErr) {
		return operationErr.Code
	}
	return ""
}
