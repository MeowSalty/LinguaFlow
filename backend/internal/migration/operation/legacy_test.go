package operation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const frozenV016Plan = `{"version":1,"operation_id":"0123456789abcdef0123456789abcdef","kind":"upgrade","route":"upgrade-v015-auth1","build_commit":"v0.16.0+abc123","created_at":"2026-10-20T12:34:56Z","source":{"config_path":"/etc/linguaflow/config.yaml","data_dir":"/srv/linguaflow","driver":"sqlite","mode":"serve","identity":"sqlite-source-identity","input_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","database_dsn":"file:/srv/linguaflow/data.sqlite?mode=ro"},"source_fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","source_schema":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","objects_digest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","target_signature":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","target":{"data_dir":"/srv/linguaflow-target","config_path":"/srv/linguaflow-target/config.yaml","rehearsal_config_path":"/srv/linguaflow-rehearsal/config.yaml","parent_identity":"target-parent-identity","identity":"target-identity","input_digest":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","rehearsal_identity":"rehearsal-identity","rehearsal_data_dir":"/srv/linguaflow-rehearsal","rehearsal_input_digest":"1111111111111111111111111111111111111111111111111111111111111111"},"accounts":[{"user_id":7,"username":"admin","email":"admin@example.test"}],"legacy_root":"/srv/legacy","storage_manifest":"/srv/operation/storage.json","storage_digest":"2222222222222222222222222222222222222222222222222222222222222222","imported_manifest":"/srv/operation/imported.json","imported_digest":"3333333333333333333333333333333333333333333333333333333333333333","storage_baseline":"4444444444444444444444444444444444444444444444444444444444444444","storage_max_ids":{"blob":91,"location":92},"external_receipt":"/srv/operation/external.json","external_digest":"5555555555555555555555555555555555555555555555555555555555555555","local_roots":[{"backend_id":"local-1","source":"/srv/storage/source","target":"/srv/storage/target","identity":"root-identity","marker_digest":"6666666666666666666666666666666666666666666666666666666666666666"}]}`

const frozenV016Journal = `{"version":1,"operation_id":"0123456789abcdef0123456789abcdef","kind":"upgrade","plan_digest":"__PLAN_DIGEST__","target_signature":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","build_commit":"v0.16.0+abc123","phase":"completed","staging":"/srv/operation/staging","staging_identity":"staging-identity","restore_identity":"restore-identity","steps":["schema","accounts","history","storage","ready"],"error_code":"","updated_at":"2026-10-20T13:34:56Z","target_fingerprint":"7777777777777777777777777777777777777777777777777777777777777777","backup_directory":"/srv/operation/backup"}`

const frozenV016Report = `{"version":1,"operation_id":"0123456789abcdef0123456789abcdef","phase":"completed","verified":true,"source_fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","counts":{"accounts":1,"resources":12},"needs_action":[],"next":["Verify the original administrator login before enabling writers."],"backup_time":"2026-10-20T12:35:00Z"}`

const frozenV016PlanDigest = "e32dbae4ac498133aff22b0958c2e5e17f2f0df14c8c918333aecd62671b0b17"

func TestReadLegacyStatusReadsFrozenV016DocumentsWithoutWriting(t *testing.T) {
	directory := writeFrozenV016Operation(t, true)
	planBytes := readTestFile(t, filepath.Join(directory, "plan.json"))
	journalBytes := readTestFile(t, filepath.Join(directory, "journal.json"))
	reportBytes := readTestFile(t, filepath.Join(directory, "report.json"))

	version, err := DocumentVersion(directory)
	if err != nil || version != 1 {
		t.Fatalf("DocumentVersion = %d, %v", version, err)
	}
	status, err := ReadLegacyStatus(directory)
	if err != nil {
		t.Fatalf("ReadLegacyStatus: %v", err)
	}
	plan := status.Plan.(*legacyPlan)
	journal := status.Journal.(*legacyJournal)
	if status.Version != 1 || status.Kind != "upgrade" || status.ExpectedBuild != "v0.16.0+abc123" {
		t.Fatalf("unexpected status header: %+v", status)
	}
	if !strings.HasSuffix(plan.Source.DatabaseDSN, "/data.sqlite?mode=ro") || !strings.HasSuffix(plan.Target.RehearsalDataDir, "linguaflow-rehearsal") || plan.Accounts[0].Email == nil || *plan.Accounts[0].Email != "admin@example.test" || plan.LocalRoots[0].MarkerDigest == "" {
		t.Fatalf("frozen v0.16 field shapes were not decoded: %+v", plan)
	}
	if got := legacyDigest(plan); journal.PlanDigest != got {
		t.Fatalf("runtime fixture digest = %s, journal %s", got, journal.PlanDigest)
	}
	if status.ReportPath != filepath.Join(directory, "report.json") || len(status.Next) == 0 {
		t.Fatalf("missing report path or next action: %+v", status)
	}
	for path, want := range map[string][]byte{
		filepath.Join(directory, "plan.json"):    planBytes,
		filepath.Join(directory, "journal.json"): journalBytes,
		filepath.Join(directory, "report.json"):  reportBytes,
	} {
		if got := readTestFile(t, path); !bytes.Equal(got, want) {
			t.Fatalf("read changed %s", filepath.Base(path))
		}
	}
	encoded, err := json.Marshal(status)
	if err != nil || !bytes.Contains(encoded, []byte(`"operation_id"`)) || !bytes.Contains(encoded, []byte(`"plan_digest"`)) {
		t.Fatalf("status does not expose frozen plan/journal JSON: %s (%v)", encoded, err)
	}
}

func TestFrozenV016PlanDigest(t *testing.T) {
	var plan legacyPlan
	if err := json.Unmarshal([]byte(frozenV016Plan), &plan); err != nil {
		t.Fatal(err)
	}
	if got := legacyDigest(&plan); got != frozenV016PlanDigest {
		t.Fatalf("frozen v0.16 plan digest = %s, want %s", got, frozenV016PlanDigest)
	}
}

func TestReadLegacyStatusReportsMissingJournalWithoutVerificationClaim(t *testing.T) {
	directory := writeFrozenV016Operation(t, false)
	status, err := ReadLegacyStatus(directory)
	if err != nil {
		t.Fatalf("ReadLegacyStatus with missing journal: %v", err)
	}
	if status.Journal != nil || status.Plan == nil || status.ReportPath != filepath.Join(directory, "report.json") {
		t.Fatalf("missing journal status lost plan or recovery path: %+v", status)
	}
	joined := strings.Join(status.Next, " ")
	if !strings.Contains(joined, "journal.json is missing") || !strings.Contains(joined, "v0.16.0+abc123") || strings.Contains(strings.ToLower(joined), "verified") || strings.Contains(joined, "已验证") {
		t.Fatalf("missing-journal guidance is incomplete or claims verification: %s", joined)
	}
}

func TestLegacyReaderRejectsTamperingAndDuplicateFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "plan content", mutate: func(value string) string {
			return strings.Replace(value, `"build_commit":"v0.16.0+abc123"`, `"build_commit":"tampered"`, 1)
		}},
		{name: "journal digest", mutate: func(value string) string {
			marker := `"plan_digest":"`
			start := strings.Index(value, marker)
			if start < 0 {
				return value
			}
			start += len(marker)
			return value[:start] + strings.Repeat("0", 64) + value[start+64:]
		}},
		{name: "duplicate plan field", mutate: func(value string) string { return strings.Replace(value, `"version":1`, `"version":1,"version":1`, 1) }},
		{name: "duplicate journal field", mutate: func(value string) string {
			return strings.Replace(value, `"phase":"completed"`, `"phase":"completed","phase":"ready"`, 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := writeFrozenV016Operation(t, true)
			path := filepath.Join(directory, "journal.json")
			value := string(readTestFile(t, path))
			if strings.Contains(test.name, "plan content") || strings.Contains(test.name, "duplicate plan") {
				path = filepath.Join(directory, "plan.json")
				value = string(readTestFile(t, path))
			}
			updated := test.mutate(value)
			if updated == value {
				t.Fatal("fixture mutation did not match")
			}
			if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadLegacyStatus(directory); err == nil {
				t.Fatal("ReadLegacyStatus accepted modified or ambiguous v1 documents")
			}
		})
	}
}

func TestDocumentVersionRejectsMalformedAndDuplicateJSON(t *testing.T) {
	for index, data := range []string{`{"version":1,"version":1}`, `{"version":`, `[]`, `{"version":"1"}`} {
		nameHash := sha256.Sum256([]byte(data))
		t.Run(string(rune('a'+index))+"-"+hex.EncodeToString(nameHash[:3]), func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "plan.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := DocumentVersion(directory); err == nil {
				t.Fatalf("DocumentVersion accepted %q", data)
			}
		})
	}
}

func TestDocumentVersionIdentifiesOtherVersionsWithoutConverting(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "plan.json"), []byte(`{"version":2,"future_field":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	version, err := DocumentVersion(directory)
	if err != nil || version != 2 {
		t.Fatalf("DocumentVersion = %d, %v", version, err)
	}
	if _, err := ReadLegacyStatus(directory); errorCode(err) != Incompatible {
		t.Fatalf("ReadLegacyStatus error = %v, want incompatible", err)
	}
}

func writeFrozenV016Operation(t *testing.T, withJournal bool) string {
	t.Helper()
	directory := t.TempDir()
	plan := strings.ReplaceAll(frozenV016Plan, "/srv", filepath.ToSlash(directory))
	plan = strings.ReplaceAll(plan, "/etc/linguaflow/config.yaml", filepath.ToSlash(filepath.Join(directory, "config.yaml")))
	var document legacyPlan
	if err := json.Unmarshal([]byte(plan), &document); err != nil {
		t.Fatal(err)
	}
	digest := legacyDigest(&document)
	journal := strings.Replace(frozenV016Journal, "__PLAN_DIGEST__", digest, 1)
	writeTestFile(t, filepath.Join(directory, "plan.json"), plan)
	if withJournal {
		writeTestFile(t, filepath.Join(directory, "journal.json"), journal)
		writeTestFile(t, filepath.Join(directory, "report.json"), frozenV016Report)
	}
	return directory
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
