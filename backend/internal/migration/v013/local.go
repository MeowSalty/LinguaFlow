package v013

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

type LocalOptions struct {
	DataDir string
	Apply   bool
}

type LocalReport struct {
	Report
	BackupDir string `json:"backup_dir,omitempty"`
	Recovery  string `json:"recovery"`
	Applied   bool   `json:"applied"`
}

type localJournal struct {
	Version        int                      `json:"version"`
	RunID          string                   `json:"run_id"`
	SourceDir      string                   `json:"source_dir"`
	StageDir       string                   `json:"stage_dir"`
	BackupDir      string                   `json:"backup_dir"`
	SourceIdentity string                   `json:"source_identity"`
	StageIdentity  string                   `json:"stage_identity"`
	Report         Report                   `json:"report"`
	Mode           string                   `json:"mode,omitempty"`
	Credentials    *sqliteCredentialBinding `json:"credentials,omitempty"`
}

type localReceipt struct {
	Version  int    `json:"version"`
	RunID    string `json:"run_id"`
	Prepared bool   `json:"prepared"`
}

type sqliteReceipt struct {
	localReceipt
	Mode        string                   `json:"mode,omitempty"`
	Credentials *sqliteCredentialBinding `json:"credentials,omitempty"`
}

// RunLocal migrates an offline local instance without changing its configured
// path. The original directory becomes the complete backup only after its
// replacement has passed validation. Never run the old service concurrently.
// An interrupted switch is recovered and reported as a separate operation;
// the same invocation never proceeds to migrate again after recovery.
func RunLocal(ctx context.Context, options LocalOptions) (result LocalReport, err error) {
	return RunSQLite(ctx, SQLiteOptions{DataDir: options.DataDir, Apply: options.Apply, Mode: config.ModeLocal})
}

// RunSQLite converts an offline SQLite instance in an unpublished directory.
// Mode is explicit: the local account policy never applies to a server instance.
func RunSQLite(ctx context.Context, options SQLiteOptions) (result LocalReport, err error) {
	return runSQLite(ctx, options, cleanupUnpublishedStaging)
}

func runSQLite(ctx context.Context, options SQLiteOptions, cleanup func(localJournal) error) (result LocalReport, err error) {
	result.Recovery = "none"
	if options.KeyringFile != "" {
		options.KeyringFile = filepath.Clean(options.KeyringFile)
	}
	if err := options.validate(); err != nil {
		return result, err
	}
	source, parent, err := localPaths(options.DataDir)
	if err != nil {
		return result, err
	}
	options.DataDir = source
	binding, err := options.binding()
	if err != nil {
		return result, err
	}
	base := filepath.Base(source)
	journalPath := filepath.Join(parent, "."+base+".v013-switch.json")
	lockPath := filepath.Join(parent, "."+base+".v013-migration.lock")
	unlock, err := acquireLocalLock(lockPath)
	if err != nil {
		return result, fmt.Errorf("acquire migration lock %q: %w", lockPath, err)
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if recovered, exists, err := recoverSQLite(source, journalPath, options.Apply, options.Mode, binding); exists || err != nil {
		if err != nil {
			err = fmt.Errorf("recover SQLite migration using %s: %w", journalPath, err)
		}
		return recovered, err
	}
	if err := validatePath(source, true); err != nil {
		return result, err
	}
	runID, err := newRunID()
	if err != nil {
		return result, err
	}
	journal := localJournal{Version: 2, RunID: runID, SourceDir: source, Mode: options.Mode, Credentials: binding,
		StageDir:  filepath.Join(parent, "."+base+".v013-stage-"+runID),
		BackupDir: filepath.Join(parent, base+".v013-backup-"+runID)}
	journal.SourceIdentity, err = directoryIdentity(source)
	if err != nil {
		return result, err
	}
	if err := credential.CreatePrivateDirectory(journal.StageDir); err != nil {
		return result, err
	}
	journal.StageIdentity, err = directoryIdentity(journal.StageDir)
	if err != nil {
		return result, errors.Join(err, os.Remove(journal.StageDir))
	}
	journalPublished := false
	defer func() {
		if !journalPublished {
			err = errors.Join(err, cleanup(journal))
		}
	}()
	if strings.SplitN(journal.SourceIdentity, ":", 2)[0] != strings.SplitN(journal.StageIdentity, ":", 2)[0] {
		return result, errors.New("the source data directory must be on the same filesystem as its parent")
	}
	if err := snapshotSQLite(ctx, filepath.Join(source, localDatabaseName), filepath.Join(journal.StageDir, localDatabaseName)); err != nil {
		return result, fmt.Errorf("snapshot old SQLite database: %w", err)
	}
	if err := copySQLiteFiles(ctx, source, journal.StageDir, options.Mode == config.ModeLocal, options.KeyringFile); err != nil {
		return result, fmt.Errorf("copy local data files: %w", err)
	}
	result.Report, err = migrateSQLiteStagingWithOptions(ctx, journal.StageDir, options)
	if err != nil {
		return result, fmt.Errorf("validate migrated SQLite staging directory: %w", err)
	}
	if !options.Apply {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if actual, err := directoryIdentity(source); err != nil || actual != journal.SourceIdentity {
		return result, errors.New("original data directory changed during migration; publication refused")
	}
	journal.Report = result.Report
	if err := options.publishExternalKeyring(); err != nil {
		return result, fmt.Errorf("publish SQLite migration keyring; retain any published key file for retry: %w", err)
	}
	if binding != nil && !binding.JWTProvided {
		secret, err := readSQLiteJWT(filepath.Join(journal.StageDir, "jwt-secret"))
		if err != nil {
			return result, err
		}
		binding.JWTDigest = secretDigest(secret)
	}
	if err := validateSQLiteRecoveryCredentials(journal.StageDir, journal); err != nil {
		return result, err
	}
	if err := publishLocalJSON(filepath.Join(journal.StageDir, localReceiptName), sqliteReceipt{localReceipt: localReceipt{2, runID, true}, Mode: options.Mode, Credentials: binding}); err != nil {
		return result, err
	}
	if err := syncLocalDirectory(journal.StageDir); err != nil {
		return result, err
	}
	if err := publishLocalJSON(journalPath, journal); err != nil {
		// A file-sync failure can occur after publication. Keep staging whenever
		// a recovery journal exists instead of deleting data that it references.
		journalPublished, _ = pathExists(journalPath)
		return result, err
	}
	journalPublished = true
	if err := syncLocalDirectory(parent); err != nil {
		return result, err
	}
	if err := switchLocalDirectories(journal, renameAbsent); err != nil {
		// Once switching starts, recovery is independent of cancellation. It
		// only renames verified identities and never overwrites a new occupant.
		recovered, _, recoveryErr := recoverSQLite(source, journalPath, true, options.Mode, binding)
		if recoveryErr == nil && recovered.Applied {
			return recovered, nil
		}
		if recoveryErr == nil && recovered.Recovery == "restored" {
			return recovered, fmt.Errorf("directory switch failed; original directory restored, migration was not applied: %w", err)
		}
		return recovered, errors.Join(fmt.Errorf("directory switch failed; preserve backup %s, staging %s and journal %s: %w", journal.BackupDir, journal.StageDir, journalPath, err), recoveryErr)
	}
	result.BackupDir, result.Applied = journal.BackupDir, true
	return result, nil
}

func acquireLocalLock(path string) (func() error, error) {
	if _, err := credential.PublishPrivateFile(path, nil); err != nil {
		return nil, err
	}
	if err := validatePath(path, false); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	unlock, err := lockMigrationFile(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	return func() error { return errors.Join(unlock(), file.Close()) }, nil
}

func publishLocalJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	published, err := credential.PublishPrivateFile(path, data)
	if err != nil {
		return err
	}
	if !published {
		return fmt.Errorf("migration state path already exists: %s", path)
	}
	return nil
}

func readLocalJSON(path string, target any) (err error) {
	if err := validatePath(path, false); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	decoder := json.NewDecoder(io.LimitReader(f, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid local migration state file; preserve all migration directories")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing local migration state")
	}
	return nil
}

func switchLocalDirectories(journal localJournal, rename func(string, string) error) error {
	if err := rename(journal.SourceDir, journal.BackupDir); err != nil {
		return err
	}
	if err := syncLocalDirectory(filepath.Dir(journal.SourceDir)); err != nil {
		return err
	}
	if err := rename(journal.StageDir, journal.SourceDir); err != nil {
		return err
	}
	return syncLocalDirectory(filepath.Dir(journal.SourceDir))
}

func validateLocalJournal(source string, journal localJournal) error {
	decoded, err := hex.DecodeString(journal.RunID)
	if err != nil || len(decoded) != 12 || (journal.Version != 1 && journal.Version != 2) || journal.SourceIdentity == "" || journal.StageIdentity == "" {
		return errors.New("unsupported local migration journal")
	}
	if err := validateSQLiteBinding(journal.Version, journal.Mode, journal.Credentials); err != nil {
		return err
	}
	parent, base := filepath.Dir(source), filepath.Base(source)
	if !sameLocalPath(journal.SourceDir, source) ||
		!sameLocalPath(journal.StageDir, filepath.Join(parent, "."+base+".v013-stage-"+journal.RunID)) ||
		!sameLocalPath(journal.BackupDir, filepath.Join(parent, base+".v013-backup-"+journal.RunID)) {
		return errors.New("local migration journal paths do not match this data directory")
	}
	return nil
}

func sameLocalPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func hasLocalReceipt(dir string, journal localJournal) error {
	var receipt sqliteReceipt
	if err := readLocalJSON(filepath.Join(dir, localReceiptName), &receipt); err != nil {
		return err
	}
	if receipt.Version != journal.Version || receipt.RunID != journal.RunID || !receipt.Prepared || receipt.Mode != journal.Mode || !equalSQLiteBindings(receipt.Credentials, journal.Credentials) {
		return errors.New("local migration receipt does not match the prepared directory")
	}
	return nil
}

func matchingLocalDirectory(path, expected string) (bool, error) {
	exists, err := pathExists(path)
	if err != nil || !exists {
		return false, err
	}
	if err := validatePath(path, true); err != nil {
		return false, err
	}
	identity, err := directoryIdentity(path)
	if err != nil {
		return false, err
	}
	if identity != expected {
		return false, fmt.Errorf("directory identity changed; preserve and inspect %s", path)
	}
	return true, nil
}

func recoverLocal(source, journalPath string, apply bool) (result LocalReport, found bool, err error) {
	return recoverSQLite(source, journalPath, apply, config.ModeLocal, nil)
}

func recoverSQLite(source, journalPath string, apply bool, mode string, binding *sqliteCredentialBinding) (result LocalReport, found bool, err error) {
	result.Recovery = "none"
	exists, err := pathExists(journalPath)
	if err != nil || !exists {
		return result, exists, err
	}
	var journal localJournal
	if err := readLocalJSON(journalPath, &journal); err != nil {
		return result, true, err
	}
	if err := validateLocalJournal(source, journal); err != nil {
		return result, true, err
	}
	if err := matchSQLiteRecoveryOptions(journal, mode, binding); err != nil {
		return result, true, err
	}
	if journal.Credentials != nil && !journal.Credentials.UsesMaster && !pathInside(source, journal.Credentials.KeyringFile) {
		if err := validateSQLiteRecoveryKeyring(source, journal); err != nil {
			return result, true, err
		}
	}
	result.Report = journal.Report
	result.BackupDir = journal.BackupDir
	stageExists, err := matchingLocalDirectory(journal.StageDir, journal.StageIdentity)
	if err != nil {
		return result, true, err
	}
	backupExists, err := matchingLocalDirectory(journal.BackupDir, journal.SourceIdentity)
	if err != nil {
		return result, true, err
	}
	sourceExists, err := pathExists(source)
	if err != nil {
		return result, true, err
	}
	if sourceExists {
		if err := validatePath(source, true); err != nil {
			return result, true, err
		}
		identity, err := directoryIdentity(source)
		if err != nil {
			return result, true, err
		}
		if identity == journal.StageIdentity && !stageExists && backupExists {
			if journal.Credentials != nil && journal.Credentials.KeysDigest != binding.KeysDigest {
				return result, true, errors.New("SQLite recovery credential keys do not match the published migration")
			}
			if err := hasLocalReceipt(source, journal); err != nil {
				return result, true, err
			}
			if err := validateSQLiteRecoveryCredentials(source, journal); err != nil {
				return result, true, err
			}
			// A completed second rename is the publication boundary, even when
			// the process never printed success. Never roll this directory back.
			result.Recovery, result.Applied = "published", true
			return result, true, nil
		}
		if identity != journal.SourceIdentity || backupExists {
			return result, true, errors.New("ambiguous local migration state; original path is occupied, no directory was overwritten")
		}
	} else if !backupExists || !stageExists {
		return result, true, errors.New("ambiguous local migration state; preserve backup and staging for manual recovery")
	}
	if stageExists {
		if err := hasLocalReceipt(journal.StageDir, journal); err != nil {
			return result, true, err
		}
		if err := validateSQLiteRecoveryCredentials(journal.StageDir, journal); err != nil {
			return result, true, err
		}
	}
	if !apply {
		return result, true, errors.New("an interrupted SQLite migration needs recovery; rerun the same command and mode with --apply while the service is stopped")
	}
	if !sourceExists {
		if err := renameAbsent(journal.BackupDir, source); err != nil {
			return result, true, fmt.Errorf("could not restore original directory; retain all migration paths: %w", err)
		}
		if err := syncLocalDirectory(filepath.Dir(source)); err != nil {
			return result, true, err
		}
	}
	if stageExists {
		if err := removeStaging(journal.StageDir, filepath.Dir(source), journal.StageIdentity); err != nil {
			return result, true, err
		}
	}
	if err := os.Remove(journalPath); err != nil {
		return result, true, err
	}
	result.Recovery, result.BackupDir = "restored", ""
	return result, true, syncLocalDirectory(filepath.Dir(source))
}
