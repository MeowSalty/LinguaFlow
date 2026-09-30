package credential

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Keyring is immutable after loading. Rotation is deployed by restarting with a
// superset of keys and a new active ID, then reencrypting stored versions.
type Keyring struct {
	active string
	keys   map[string][]byte
}

func (*Keyring) String() string { return "credential keyring (redacted)" }

type keyringDocument struct {
	Version     int               `json:"version"`
	ActiveKeyID string            `json:"active_key_id"`
	Keys        map[string]string `json:"keys"`
}

func LoadKeyring(path string) (*Keyring, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, errors.New("cannot inspect credential keyring")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("credential keyring must be a regular private file")
	}
	// Check the open file we are about to read, not a second lookup of its path.
	// Diagnostics and startup share this read-only policy; neither repairs ACLs.
	if err := checkFilePermissions(f); err != nil {
		return nil, fmt.Errorf("credential keyring permissions: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, errors.New("cannot read credential keyring")
	}
	if len(data) > 1<<20 {
		return nil, errors.New("credential keyring is too large")
	}
	return ParseKeyring(data)
}

func ParseKeyring(data []byte) (*Keyring, error) {
	if err := rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return nil, errors.New("invalid credential keyring JSON")
	}
	var doc keyringDocument
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("invalid credential keyring JSON")
	}
	if doc.Version != 1 {
		return nil, errors.New("unsupported or missing credential keyring version")
	}
	if doc.ActiveKeyID == "" || len(doc.Keys) == 0 {
		return nil, ErrKeyUnavailable
	}
	k := &Keyring{active: doc.ActiveKeyID, keys: make(map[string][]byte, len(doc.Keys))}
	for id, value := range doc.Keys {
		key, err := base64.StdEncoding.DecodeString(value)
		if id == "" || err != nil || len(key) != 32 {
			return nil, errors.New("credential keys must be base64 encoded 32-byte values")
		}
		k.keys[id] = key
	}
	if !k.HasKey(k.active) {
		return nil, ErrKeyUnavailable
	}
	return k, nil
}

// rejectDuplicateKeys additionally rejects trailing documents and repeated keys.
func rejectDuplicateKeys(dec *json.Decoder) error {
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
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrInvalid
				}
				seen[name] = true
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
			return ErrInvalid
		}
		_, err = dec.Token()
		return err
	}
	if err := read(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

// PrepareKeyring publishes a fully written private file without replacing an
// existing keyring. The caller must determine allowCreate from instance state.
func PrepareKeyring(path string, allowCreate bool) (*Keyring, error) {
	k, err := LoadKeyring(path)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !allowCreate {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	data, err := json.Marshal(keyringDocument{Version: 1, ActiveKeyID: id, Keys: map[string]string{id: base64.StdEncoding.EncodeToString(key)}})
	if err != nil {
		return nil, err
	}
	if _, err := PublishPrivateFile(path, data); err != nil {
		return nil, err
	}
	return LoadKeyring(path)
}

// PublishPrivateFile atomically publishes private bytes without overwriting an
// existing path. False means a concurrent/existing owner already published it.
func PublishPrivateFile(path string, data []byte) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(dir, ".private-*")
	if err != nil {
		return false, err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err := restrictFile(temp); err != nil {
		f.Close()
		return false, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	// Hard-link publication is atomic and fails if another starter won. Unlike
	// rename on Unix this never overwrites the winner's key material.
	if err := os.Link(temp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("publish private file: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return true, err
	}
	return true, nil
}

func (k *Keyring) HasKey(id string) bool { return k != nil && len(k.keys[id]) == 32 }
func (k *Keyring) ActiveKeyID() string {
	if k == nil {
		return ""
	}
	return k.active
}

// AssociatedData is versioned and uses JSON's unambiguous string encoding.
type AssociatedData struct {
	Format   int    `json:"format"`
	ID       int    `json:"id"`
	Version  int    `json:"version"`
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Scope    string `json:"scope"`
	OwnerID  int    `json:"owner_id"`
}

func (a AssociatedData) bytes() []byte { a.Format = 1; b, _ := json.Marshal(a); return b }

type Ciphertext struct {
	Version int
	KeyID   string
	Nonce   []byte
	Data    []byte
}

func (k *Keyring) aead(id string) (cipher.AEAD, error) {
	if !k.HasKey(id) {
		return nil, ErrKeyUnavailable
	}
	block, err := aes.NewCipher(k.keys[id])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (k *Keyring) Encrypt(secret string, aad AssociatedData) (Ciphertext, error) {
	if secret == "" {
		return Ciphertext{}, ErrInvalid
	}
	a, err := k.aead(k.ActiveKeyID())
	if err != nil {
		return Ciphertext{}, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Ciphertext{}, err
	}
	return Ciphertext{Version: 1, KeyID: k.active, Nonce: nonce, Data: a.Seal(nil, nonce, []byte(secret), aad.bytes())}, nil
}
func (k *Keyring) Decrypt(value Ciphertext, aad AssociatedData) (string, error) {
	if value.Version != 1 {
		return "", ErrDecrypt
	}
	a, err := k.aead(value.KeyID)
	if err != nil {
		return "", err
	}
	if len(value.Nonce) != a.NonceSize() {
		return "", ErrDecrypt
	}
	plain, err := a.Open(nil, value.Nonce, value.Data, aad.bytes())
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}
