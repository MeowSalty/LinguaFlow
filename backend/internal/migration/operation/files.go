package operation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func Decode(data []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := rejectDuplicates(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return Fail(Incompatible, "duplicate or malformed JSON")
	}
	if err := dec.Decode(value); err != nil {
		return Fail(Incompatible, "invalid persistent document")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Fail(Incompatible, "trailing data in persistent document")
	}
	return nil
}

func rejectDuplicates(dec *json.Decoder) error {
	var read func() error
	read = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return errors.New("duplicate key")
				}
				seen[k] = true
				if err := read(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := read(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid delimiter")
		}
		_, err = dec.Token()
		return err
	}
	if err := read(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing document")
	}
	return nil
}

func Read(path string, value any) error {
	if err := ValidatePath(path, false); err != nil {
		return err
	}
	f, err := openRead(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 64<<20 {
		return Fail(Incompatible, "document exceeds size limit")
	}
	return Decode(data, value)
}

// Write publishes a fully synced private file in an operation-owned directory.
func Write(path string, value any, create bool) error {
	if err := ValidatePath(filepath.Dir(path), true); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if create {
		ok, err := credential.PublishPrivateFile(path, data)
		if err != nil {
			return err
		}
		if !ok {
			return Fail(Incompatible, "operation artifact already exists")
		}
		return nil
	}
	if err := ValidatePath(path, false); err != nil {
		return err
	}
	nameID, err := NewID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), ".migration-"+nameID)
	ok, err := credential.PublishPrivateFile(temporary, data)
	if err != nil {
		return err
	}
	if !ok {
		return Fail(Incompatible, "temporary artifact collision")
	}
	defer os.Remove(temporary)
	if err := replaceFile(temporary, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func Create(directory string, p *Plan) (*Journal, error) {
	p.Directory = directory
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if err := ValidatePath(filepath.Dir(directory), true); err != nil {
		return nil, err
	}
	if err := credential.CreatePrivateDirectory(directory); err != nil {
		return nil, err
	}
	if err := Write(filepath.Join(directory, "plan.json"), p, true); err != nil {
		return nil, err
	}
	j := &Journal{Version: Version, ID: p.ID, Kind: p.Kind, PlanDigest: Digest(p), TargetSignature: p.TargetSignature, Build: p.Build, Phase: "planned", UpdatedAt: time.Now().UTC()}
	if err := Write(filepath.Join(directory, "journal.json"), j, true); err != nil {
		return nil, err
	}
	return j, nil
}

func Load(directory string) (*Plan, *Journal, error) {
	version, err := DocumentVersion(directory)
	if err != nil {
		return nil, nil, err
	}
	if version == 1 {
		var document map[string]json.RawMessage
		if err := Read(filepath.Join(directory, "plan.json"), &document); err != nil {
			return nil, nil, err
		}
		var build string
		if err := json.Unmarshal(document["build_commit"], &build); err != nil || build == "" {
			return nil, nil, Fail(Incompatible, "preserve this v1 operation and use its original binary")
		}
		return nil, nil, Fail(Incompatible, "v1 operation is read-only in this tool; use the exact recorded original binary "+build+" for resume or recovery")
	}
	if version != Version {
		return nil, nil, Fail(Incompatible, "unsupported operation document version")
	}
	p := new(Plan)
	if err := Read(filepath.Join(directory, "plan.json"), p); err != nil {
		return nil, nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}
	p.Directory = directory
	j := new(Journal)
	if err := Read(filepath.Join(directory, "journal.json"), j); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		// A lost filesystem journal never erases committed database receipts.
		// The runner must still prove ownership and load the original protection.
		j = &Journal{Version: Version, ID: p.ID, Kind: p.Kind, PlanDigest: Digest(p), TargetSignature: p.TargetSignature, Build: p.Build, Phase: "planned", UpdatedAt: time.Now().UTC()}
	}
	if err := j.Bind(p); err != nil {
		return nil, nil, err
	}
	return p, j, nil
}

func SaveJournal(directory string, j *Journal) error {
	j.UpdatedAt = time.Now().UTC()
	path := filepath.Join(directory, "journal.json")
	_, err := os.Lstat(path)
	return Write(path, j, errors.Is(err, os.ErrNotExist))
}

func ValidatePath(path string, directory bool) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return Fail(Incompatible, "ordinary absolute paths are required")
	}
	path = filepath.Clean(path)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return Fail(Incompatible, "links are not accepted in operation paths")
		}
		if err := ordinaryPath(current); err != nil {
			return err
		}
		if current == path {
			if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
				return Fail(Incompatible, "unexpected file type")
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

func Overlap(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b)) {
		a, b = foldPath(a), foldPath(b)
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func Lock(directory string) (func() error, error) {
	if err := ValidatePath(directory, true); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "operation.lock")
	return LockPath(path)
}

// LockPath serializes offline operations selecting the same SQLite database,
// even when their operation directories differ. Read commands never call it.
func LockPath(path string) (func() error, error) {
	if err := ValidatePath(filepath.Dir(path), true); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		ok, publishErr := credential.PublishPrivateFile(path, []byte("migration operation lock\n"))
		if publishErr != nil {
			return nil, publishErr
		}
		_ = ok
	} else if err != nil {
		return nil, err
	}
	if err := ValidatePath(path, false); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(f)
	if err != nil {
		f.Close()
		return nil, Fail(NeedsAction, "another process holds this operation lock")
	}
	return func() error { return errors.Join(unlock(), f.Close()) }, nil
}

func PublishDirectory(staging, target, identity, parentIdentity string) error {
	if err := ValidatePath(staging, true); err != nil {
		return err
	}
	actual, err := Identity(staging)
	if err != nil || actual != identity {
		return Fail(BaselineChanged, "staging directory identity changed")
	}
	if err := ValidatePath(filepath.Dir(target), true); err != nil {
		return err
	}
	actual, err = Identity(filepath.Dir(target))
	if err != nil || actual != parentIdentity {
		return Fail(BaselineChanged, "target parent identity changed")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return Fail(TargetNotEmpty, "target directory appeared; preserve staging and choose a new target")
	}
	if strings.Split(identity, ":")[0] != strings.Split(parentIdentity, ":")[0] {
		return Fail(UnsupportedTarget, "publication must stay on one volume")
	}
	if err := renameNoReplace(staging, target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

// MoveOwnedFile never replaces an occupied destination. Restoration uses it
// after connections close, with an intent binding both file identities.
func MoveOwnedFile(source, target, identity string) error {
	if err := ValidatePath(source, false); err != nil {
		return err
	}
	if err := ValidatePath(filepath.Dir(target), true); err != nil {
		return err
	}
	actual, err := Identity(source)
	if err != nil || actual != identity {
		return Fail(BaselineChanged, "recovery file identity changed")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return Fail(TargetNotEmpty, "recovery destination is occupied")
	}
	if err := renameNoReplace(source, target); err != nil {
		return err
	}
	return errors.Join(syncDir(filepath.Dir(source)), syncDir(filepath.Dir(target)))
}
