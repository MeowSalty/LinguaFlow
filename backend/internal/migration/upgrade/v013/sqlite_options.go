package v013

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

// SQLiteOptions selects the original instance mode and deployment credentials.
// For server mode Keys is required. A nonempty KeyringFile selects a file;
// otherwise Keys must be retained by the caller as its explicit master key.
// PendingKeyring contains the exact bytes to publish if that file is absent.
// An empty JWTSecret creates or reuses data-dir/jwt-secret only in staging.
type SQLiteOptions struct {
	DataDir        string
	Apply          bool
	Mode           string
	Keys           *credential.Keyring
	KeyringFile    string
	PendingKeyring []byte
	JWTSecret      string
}

// Bind recovery to deployment choices without retaining secret material.
type sqliteCredentialBinding struct {
	KeyringFile string `json:"keyring_file,omitempty"`
	UsesMaster  bool   `json:"uses_master"`
	JWTProvided bool   `json:"jwt_provided"`
	KeysDigest  string `json:"keys_digest"`
	JWTDigest   string `json:"jwt_digest"`
}

// ResolveSQLiteInputPath locates an explicit secret file during an interrupted
// directory switch. The caller retains the original deployment path in its
// options and uses this path only for reading. Recovery itself remains locked
// and validates the requested mode and credentials before changing anything.
func ResolveSQLiteInputPath(dataDir, inputPath string) (string, error) {
	source, parent, err := localPaths(dataDir)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(inputPath)
	if err != nil {
		return "", err
	}
	if !pathInside(source, path) {
		return path, nil
	}
	exists, err := pathExists(source)
	if err != nil || exists {
		return path, err
	}
	journalPath := filepath.Join(parent, "."+filepath.Base(source)+".v013-switch.json")
	var journal localJournal
	if err := readLocalJSON(journalPath, &journal); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", fmt.Errorf("locate SQLite recovery secret input: %w", err)
	}
	if err := validateLocalJournal(source, journal); err != nil {
		return "", err
	}
	for _, directory := range []struct{ path, identity string }{{journal.StageDir, journal.StageIdentity}, {journal.BackupDir, journal.SourceIdentity}} {
		matches, err := matchingLocalDirectory(directory.path, directory.identity)
		if err != nil {
			return "", err
		}
		if !matches {
			return "", errors.New("SQLite recovery secret input requires the verified staging and backup directories")
		}
	}
	if err := hasLocalReceipt(journal.StageDir, journal); err != nil {
		return "", err
	}
	if err := validateSQLiteRecoveryCredentials(journal.StageDir, journal); err != nil {
		return "", err
	}
	return stagedKeyringPath(source, journal.StageDir, path), nil
}

func (o SQLiteOptions) validate() error {
	if o.Mode != config.ModeLocal && o.Mode != config.ModeServer {
		return errors.New("SQLite migration requires an explicit server or local mode")
	}
	if o.Mode == config.ModeLocal {
		if o.Keys != nil || o.KeyringFile != "" || len(o.PendingKeyring) != 0 || o.JWTSecret != "" {
			return errors.New("local migration uses only credentials inside its data directory")
		}
		return nil
	}
	if _, err := credential.EncodeKeyring(o.Keys); err != nil {
		return fmt.Errorf("SQLite server migration credential keys: %w", err)
	}
	if o.JWTSecret != "" && len(o.JWTSecret) < 32 {
		return errors.New("SQLite server migration JWT secret must contain at least 32 bytes")
	}
	if o.KeyringFile != "" && (!filepath.IsAbs(o.KeyringFile) || filepath.Clean(o.KeyringFile) != o.KeyringFile) {
		return errors.New("SQLite migration keyring path must be an absolute clean path")
	}
	if len(o.PendingKeyring) != 0 {
		if o.KeyringFile == "" {
			return errors.New("pending SQLite migration keyring requires a keyring path")
		}
		pending, err := credential.ParseKeyring(o.PendingKeyring)
		if err != nil {
			return fmt.Errorf("pending SQLite migration keyring: %w", err)
		}
		if err := equalSQLiteKeys(pending, o.Keys); err != nil {
			return err
		}
	}
	return nil
}

func secretDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func keyringDigest(keys *credential.Keyring) (string, error) {
	encoded, err := credential.EncodeKeyring(keys)
	if err != nil {
		return "", err
	}
	return secretDigest(string(encoded)), nil
}

func equalSQLiteKeys(a, b *credential.Keyring) error {
	left, err := credential.EncodeKeyring(a)
	if err != nil {
		return err
	}
	right, err := credential.EncodeKeyring(b)
	if err != nil {
		return err
	}
	if !bytes.Equal(left, right) {
		return errors.New("SQLite migration keyring does not match the selected credential keys")
	}
	return nil
}

func (o SQLiteOptions) binding() (*sqliteCredentialBinding, error) {
	if o.Mode == config.ModeLocal {
		return nil, nil
	}
	if sameLocalPath(o.DataDir, o.KeyringFile) {
		return nil, errors.New("SQLite migration keyring must be a file, not the data directory")
	}
	if o.KeyringFile != "" {
		base, parent := filepath.Base(o.DataDir), filepath.Dir(o.DataDir)
		reserved := []string{
			filepath.Join(parent, "."+base+".v013-migration.lock"),
			filepath.Join(parent, "."+base+".v013-switch.json"),
		}
		for _, name := range []string{localDatabaseName, localDatabaseName + "-wal", localDatabaseName + "-shm", localDatabaseName + "-journal", localReceiptName, "instance-secret", "jwt-secret"} {
			reserved = append(reserved, filepath.Join(o.DataDir, name))
		}
		for _, path := range reserved {
			if sameLocalPath(path, o.KeyringFile) {
				return nil, errors.New("SQLite migration keyring path collides with a database, secret, or migration state file")
			}
		}
		if err := validateSQLiteKeyringPath(o.KeyringFile); err != nil {
			return nil, err
		}
	}
	digest, err := keyringDigest(o.Keys)
	if err != nil {
		return nil, err
	}
	binding := &sqliteCredentialBinding{KeyringFile: o.KeyringFile, UsesMaster: o.KeyringFile == "", JWTProvided: o.JWTSecret != "", KeysDigest: digest}
	if binding.JWTProvided {
		binding.JWTDigest = secretDigest(o.JWTSecret)
	}
	return binding, nil
}

func pathInside(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && filepath.IsLocal(relative)
}

func stagedKeyringPath(source, stage, keyring string) string {
	if pathInside(source, keyring) {
		relative, _ := filepath.Rel(source, keyring)
		return filepath.Join(stage, relative)
	}
	return keyring
}

func prepareSQLiteStagingConfig(dir string, o SQLiteOptions) (*config.ServerConfig, *credential.Keyring, error) {
	// Deployment configuration must never be discovered from the migration
	// process environment. All operational defaults come from the same config
	// constructor used by the server resolver; only selected inputs are applied.
	cfg := config.DefaultServerConfig()
	cfg.DataDir, cfg.Mode = dir, o.Mode
	cfg.Workers.Translation.QueueCapacity = cfg.Workers.Translation.Count * 4
	cfg.Workers.Sync.QueueCapacity = cfg.Workers.Sync.Count * 8
	cfg.SSE.MaxReplayEvents = cfg.SSE.RingBufferCapacity * 2
	var keys *credential.Keyring
	var err error
	if o.Mode == config.ModeLocal {
		cfg.Host, cfg.Port = "127.0.0.1", 18080
		cfg.JWTSecret, err = config.PrepareLocalSecret(filepath.Join(dir, "instance-secret"))
		if err != nil {
			return nil, nil, err
		}
		cfg.Credentials.KeyringFile = filepath.Join(dir, "credentials-keyring.json")
		keys, err = credential.PrepareKeyring(cfg.Credentials.KeyringFile, true)
	} else {
		keys = o.Keys
		cfg.JWTSecret = o.JWTSecret
		if cfg.JWTSecret == "" {
			cfg.JWTSecret, err = prepareSQLiteJWT(filepath.Join(dir, "jwt-secret"))
			if err != nil {
				return nil, nil, err
			}
		}
		cfg.Credentials.KeyringFile = stagedKeyringPath(o.DataDir, dir, o.KeyringFile)
		if o.KeyringFile != "" {
			if pathInside(o.DataDir, o.KeyringFile) && len(o.PendingKeyring) != 0 {
				if _, err := credential.PublishPrivateFile(cfg.Credentials.KeyringFile, o.PendingKeyring); err != nil {
					return nil, nil, err
				}
			}
			err = o.verifyKeyring(cfg.Credentials.KeyringFile, !pathInside(o.DataDir, o.KeyringFile))
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if err := config.ValidateServerConfig(cfg); err != nil {
		return nil, nil, err
	}
	return cfg, keys, nil
}

func readSQLiteJWT(path string) (string, error) {
	if err := validatePath(path, false); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := string(data)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	if len(value) < 32 {
		return "", errors.New("SQLite server migration JWT secret file must contain at least 32 bytes")
	}
	return value, nil
}

func prepareSQLiteJWT(path string) (string, error) {
	value, err := readSQLiteJWT(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return value, err
	}
	value, err = credential.GenerateMasterKey()
	if err != nil {
		return "", err
	}
	if _, err := credential.PublishPrivateFile(path, []byte(value+"\n")); err != nil {
		return "", err
	}
	return readSQLiteJWT(path)
}

func (o SQLiteOptions) verifyKeyring(path string, allowPending bool) error {
	if err := validateSQLiteKeyringPath(path); err != nil {
		return err
	}
	keys, err := credential.LoadKeyring(path)
	if allowPending && len(o.PendingKeyring) != 0 && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return equalSQLiteKeys(keys, o.Keys)
}

func validateSQLiteKeyringPath(path string) error {
	if err := validateSQLitePathSpelling(path); err != nil {
		return err
	}
	if err := validatePath(path, false); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Check every existing ancestor even when the target's parent must be
		// created later. Never publish through a symlink or junction.
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			if err := validatePath(parent, true); err == nil {
				return nil
			} else if !errors.Is(err, os.ErrNotExist) || parent == filepath.Dir(parent) {
				return err
			}
		}
	}
	return nil
}

func (o SQLiteOptions) publishExternalKeyring() error {
	if o.Mode != config.ModeServer || o.KeyringFile == "" || pathInside(o.DataDir, o.KeyringFile) {
		return nil
	}
	if err := o.verifyKeyring(o.KeyringFile, true); err != nil {
		return err
	}
	if len(o.PendingKeyring) != 0 {
		if _, err := credential.PublishPrivateFile(o.KeyringFile, o.PendingKeyring); err != nil {
			return err
		}
	}
	return o.verifyKeyring(o.KeyringFile, false)
}

func equalSQLiteBindings(a, b *sqliteCredentialBinding) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func validDigest(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == sha256.Size
}

func validateSQLiteBinding(version int, mode string, binding *sqliteCredentialBinding) error {
	if (version == 1 && mode == "" && binding == nil) || (version == 2 && mode == config.ModeLocal && binding == nil) {
		return nil
	}
	if version != 2 || mode != config.ModeServer || binding == nil ||
		binding.UsesMaster != (binding.KeyringFile == "") || !validDigest(binding.KeysDigest) || !validDigest(binding.JWTDigest) ||
		(binding.KeyringFile != "" && (!filepath.IsAbs(binding.KeyringFile) || filepath.Clean(binding.KeyringFile) != binding.KeyringFile)) {
		return errors.New("unsupported SQLite migration mode or credential binding")
	}
	return nil
}

func matchSQLiteRecoveryOptions(journal localJournal, mode string, expected *sqliteCredentialBinding) error {
	savedMode := journal.Mode
	if journal.Version == 1 && savedMode == "" {
		savedMode = config.ModeLocal
	}
	if savedMode != mode {
		return errors.New("SQLite recovery mode does not match the prepared migration; rerun with the original mode")
	}
	if mode == config.ModeLocal {
		return nil
	}
	saved := journal.Credentials
	if saved == nil || expected == nil || !sameLocalPath(saved.KeyringFile, expected.KeyringFile) ||
		saved.UsesMaster != expected.UsesMaster || saved.JWTProvided != expected.JWTProvided ||
		((saved.UsesMaster || !pathInside(journal.SourceDir, saved.KeyringFile)) && saved.KeysDigest != expected.KeysDigest) ||
		(saved.JWTProvided && saved.JWTDigest != expected.JWTDigest) {
		return errors.New("SQLite recovery credential configuration does not match the prepared migration; retain the original key and JWT configuration")
	}
	return nil
}

func validateSQLiteRecoveryCredentials(dir string, journal localJournal) error {
	binding := journal.Credentials
	if binding == nil {
		return nil
	}
	if err := validateSQLiteRecoveryKeyring(dir, journal); err != nil {
		return err
	}
	if !binding.JWTProvided {
		secret, err := readSQLiteJWT(filepath.Join(dir, "jwt-secret"))
		if err != nil {
			return err
		}
		if secretDigest(secret) != binding.JWTDigest {
			return errors.New("SQLite recovery JWT secret does not match the prepared migration; preserve all migration paths")
		}
	}
	return nil
}

func validateSQLiteRecoveryKeyring(dir string, journal localJournal) error {
	binding := journal.Credentials
	if binding == nil || binding.UsesMaster {
		return nil
	}
	path := stagedKeyringPath(journal.SourceDir, dir, binding.KeyringFile)
	if err := validatePath(path, false); err != nil {
		return err
	}
	keys, err := credential.LoadKeyring(path)
	if err != nil {
		return err
	}
	digest, err := keyringDigest(keys)
	if err != nil {
		return err
	}
	if digest != binding.KeysDigest {
		return errors.New("SQLite recovery keyring does not match the prepared migration; preserve all migration paths")
	}
	return nil
}
