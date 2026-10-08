package credential

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const masterKeyIDDomain = "linguaflow/credentials/master-key/v1\x00"

// FromMasterKey creates a single-key keyring from a canonical standard Base64
// value. The ID depends only on the decoded key, so all input sources agree.
func FromMasterKey(value string) (*Keyring, error) {
	if len(value) != base64.StdEncoding.EncodedLen(32) {
		return nil, errors.New("credential master key must be a canonical base64 encoded 32-byte value")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != value {
		return nil, errors.New("credential master key must be a canonical base64 encoded 32-byte value")
	}
	digest := sha256.Sum256(append([]byte(masterKeyIDDomain), key...))
	id := "sha256:" + hex.EncodeToString(digest[:])
	return &Keyring{active: id, keys: map[string][]byte{id: key}}, nil
}

// GenerateMasterKey returns an independently generated 256-bit random key.
func GenerateMasterKey() (string, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", errors.New("cannot generate credential master key")
	}
	return base64.StdEncoding.EncodeToString(key[:]), nil
}

// WithMasterKey retains every original ID and activates the supplied key under
// its stable ID. Neither the original keyring nor its key bytes are modified.
func (k *Keyring) WithMasterKey(value string) (*Keyring, error) {
	if k == nil || !k.HasKey(k.active) {
		return nil, ErrKeyUnavailable
	}
	next, err := FromMasterKey(value)
	if err != nil {
		return nil, err
	}
	for id, key := range k.keys {
		if id == "" || len(key) != 32 {
			return nil, ErrKeyUnavailable
		}
		if current, exists := next.keys[id]; exists && !bytes.Equal(current, key) {
			return nil, errors.New("credential key ID is already assigned to different key material")
		}
		next.keys[id] = bytes.Clone(key)
	}
	return next, nil
}

// EncodeKeyring serializes every key, including legacy IDs, without changing
// the keyring. The returned bytes contain secrets and require private storage.
// One byte is reserved for an optional final newline in the published file.
func EncodeKeyring(k *Keyring) ([]byte, error) {
	if k == nil || !k.HasKey(k.active) {
		return nil, ErrKeyUnavailable
	}
	doc := keyringDocument{Version: 1, ActiveKeyID: k.active, Keys: make(map[string]string, len(k.keys))}
	for id, key := range k.keys {
		if id == "" || len(key) != 32 {
			return nil, ErrKeyUnavailable
		}
		doc.Keys[id] = base64.StdEncoding.EncodeToString(key)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data) >= maxKeyringFileSize {
		return nil, errors.New("encoded credential keyring exceeds the 1 MiB file size limit")
	}
	return data, nil
}
