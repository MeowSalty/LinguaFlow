package credential

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testKeys(t *testing.T, active string, keys map[string][]byte) *Keyring {
	t.Helper()
	doc := keyringDocument{Version: 1, ActiveKeyID: active, Keys: map[string]string{}}
	for id, key := range keys {
		doc.Keys[id] = base64.StdEncoding.EncodeToString(key)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParseKeyring(data)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func TestEncryptionBindsIdentityAndUsesFreshNonce(t *testing.T) {
	k := testKeys(t, "first", map[string][]byte{"first": bytes.Repeat([]byte{1}, 32)})
	aad := AssociatedData{ID: 3, Version: 1, Provider: "openai", Endpoint: "https://api.openai.com/v1/", Scope: "user", OwnerID: 7}
	const secret = "sensitive-value-to-never-log"
	a, err := k.Encrypt(secret, aad)
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.Encrypt(secret, aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Nonce, b.Nonce) || bytes.Equal(a.Data, b.Data) || bytes.Contains(a.Data, []byte(secret)) {
		t.Fatal("encryption did not randomize/hide plaintext")
	}
	plain, err := k.Decrypt(a, aad)
	if err != nil || plain != secret {
		t.Fatalf("decrypt: %v", err)
	}
	for _, change := range []func(*AssociatedData){func(a *AssociatedData) { a.ID++ }, func(a *AssociatedData) { a.Version++ }, func(a *AssociatedData) { a.OwnerID++ }, func(a *AssociatedData) { a.Scope = "org" }, func(a *AssociatedData) { a.Provider = "google" }, func(a *AssociatedData) { a.Endpoint = "https://other.example/" }} {
		wrong := aad
		change(&wrong)
		if _, err := k.Decrypt(a, wrong); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("changed AAD accepted: %v", err)
		}
	}
	a.Data[0] ^= 1
	if _, err := k.Decrypt(a, aad); !errors.Is(err, ErrDecrypt) || strings.Contains(err.Error(), secret) {
		t.Fatalf("tampering exposed/accepted: %v", err)
	}
}
func TestKeyRotationCanReadOldAndReencrypt(t *testing.T) {
	oldKey := bytes.Repeat([]byte{1}, 32)
	newKey := bytes.Repeat([]byte{2}, 32)
	old := testKeys(t, "old", map[string][]byte{"old": oldKey})
	next := testKeys(t, "new", map[string][]byte{"old": oldKey, "new": newKey})
	aad := AssociatedData{ID: 1, Version: 2, Provider: "google", Endpoint: "https://example.test/", Scope: "org", OwnerID: 2}
	encrypted, err := old.Encrypt("key", aad)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := next.Decrypt(encrypted, aad)
	if err != nil {
		t.Fatal(err)
	}
	rewritten, err := next.Encrypt(plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.KeyID != "new" || bytes.Equal(encrypted.Nonce, rewritten.Nonce) {
		t.Fatal("reencryption must use active key and new nonce")
	}
	withoutOld := testKeys(t, "new", map[string][]byte{"new": newKey})
	if _, err := withoutOld.Decrypt(encrypted, aad); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("missing key: %v", err)
	}
	if got, err := withoutOld.Decrypt(rewritten, aad); err != nil || got != "key" {
		t.Fatalf("new ciphertext: %v", err)
	}
}
func TestKeyringRejectsMalformedAndSecretErrors(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	for _, raw := range []string{`{}`, `{"version":1,"active_key_id":"a","keys":{"a":"bad-secret-key"}}`, `{"version":1,"active_key_id":"a","keys":{"b":"` + key + `"}}`, `{"version":1,"active_key_id":"a","active_key_id":"b","keys":{"a":"` + key + `"}}`, `{"version":1,"active_key_id":"a","keys":{"a":"` + key + `","a":"` + key + `"}}`, `{"version":1,"active_key_id":"a","keys":{"a":"` + key + `"},"unknown":true}`, `{"version":1,"active_key_id":"a","keys":{"a":"` + key + `"}} {}`} {
		if _, err := ParseKeyring([]byte(raw)); err == nil || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "bad-secret-key") {
			t.Fatalf("invalid keyring accepted/leaked: %v", err)
		}
	}
}
func TestPrepareKeyringNoSideEffectsOnReadAndConcurrentPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "keyring.json")
	if _, err := PrepareKeyring(path, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing keyring: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created directory")
	}
	var wg sync.WaitGroup
	results := make(chan *Keyring, 10)
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := PrepareKeyring(path, true)
			if err != nil {
				errs <- err
				return
			}
			results <- k
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	first := ""
	for k := range results {
		if first == "" {
			first = k.active
		}
		if k.active != first {
			t.Fatal("concurrent preparation replaced the winning key")
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareKeyring(path, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("restart changed keyring")
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareKeyring(path, true); err == nil {
		t.Fatal("corrupt keyring silently replaced")
	}
}
