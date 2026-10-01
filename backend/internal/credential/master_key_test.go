package credential

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestMasterKeyStableIDAndCanonicalInput(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	value := base64.StdEncoding.EncodeToString(key)
	first, err := FromMasterKey(value)
	if err != nil {
		t.Fatal(err)
	}
	const expectedID = "sha256:b67adcd3c846ff5411340d338cd4d87be92aae02dd7841215a0105f593b3804f"
	if first.ActiveKeyID() != expectedID {
		t.Fatalf("stable key ID changed: %s", first.ActiveKeyID())
	}
	second, err := FromMasterKey(value)
	if err != nil || second.ActiveKeyID() != expectedID {
		t.Fatalf("repeated construction changed the ID: %v", err)
	}
	aad := AssociatedData{ID: 1, Version: 1, Provider: "openai", Scope: "user", OwnerID: 1}
	encrypted, err := first.Encrypt("provider-token", aad)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := second.Decrypt(encrypted, aad); err != nil || plain != "provider-token" {
		t.Fatalf("reconstructed key could not decrypt: %v", err)
	}
	for name, invalid := range map[string]string{
		"empty": "", "short": base64.StdEncoding.EncodeToString(key[:31]),
		"long":     base64.StdEncoding.EncodeToString(append(key, 0)),
		"unpadded": strings.TrimSuffix(value, "="), "newline": value + "\n",
		"embedded_newline": value[:4] + "\r\n" + value[4:], "space": " " + value,
		"url_encoding":    base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{255}, 32)),
		"nonzero_padding": value[:42] + "9=", "plaintext": "not-a-base64-master-key",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := FromMasterKey(invalid)
			if err == nil {
				t.Fatal("noncanonical or malformed master key accepted")
			}
			if invalid != "" && strings.Contains(err.Error(), invalid) {
				t.Fatal("validation error exposes master key")
			}
		})
	}
}

func TestGeneratedKeyringUsesMasterKeyID(t *testing.T) {
	first, err := GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateMasterKey()
	if err != nil || first == second {
		t.Fatalf("random key generation failed: %v", err)
	}
	if _, err := FromMasterKey(first); err != nil {
		t.Fatal(err)
	}
	keys, data, err := GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := ParseKeyring(data)
	if err != nil {
		t.Fatal(err)
	}
	single, err := FromMasterKey(base64.StdEncoding.EncodeToString(keys.keys[keys.active]))
	if err != nil || single.active != keys.active || reloaded.active != keys.active {
		t.Fatalf("generated keyring does not preserve stable master key ID: %v", err)
	}
}

func TestWithMasterKeyPreservesLegacyIDsAndOriginalKeyring(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	value := base64.StdEncoding.EncodeToString(key)
	old := testKeys(t, "legacy arbitrary ID", map[string][]byte{"legacy arbitrary ID": key})
	aad := AssociatedData{ID: 1, Version: 1, Provider: "openai", Scope: "user", OwnerID: 2}
	encrypted, err := old.Encrypt("provider-key", aad)
	if err != nil {
		t.Fatal(err)
	}
	next, err := old.WithMasterKey(value)
	if err != nil {
		t.Fatal(err)
	}
	if next.active == old.active || !next.HasKey(old.active) || len(next.keys) != 2 || len(old.keys) != 1 {
		t.Fatal("adding stable ID changed or discarded legacy IDs")
	}
	if plain, err := next.Decrypt(encrypted, aad); err != nil || plain != "provider-key" {
		t.Fatalf("legacy ciphertext could not decrypt: %v", err)
	}
	reused, err := next.WithMasterKey(value)
	if err != nil || reused.active != next.active || len(reused.keys) != 2 {
		t.Fatalf("existing matching key was not reused: %v", err)
	}
	reused.keys[old.active][0] ^= 1
	if !bytes.Equal(next.keys[old.active], key) || !bytes.Equal(old.keys[old.active], key) {
		t.Fatal("keyring copies share mutable key bytes")
	}
	encoded, err := EncodeKeyring(next)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := ParseKeyring(encoded)
	if err != nil || reloaded.active != next.active || !reloaded.HasKey(old.active) {
		t.Fatalf("encoding lost active or legacy key IDs: %v", err)
	}
	if plain, err := reloaded.Decrypt(encrypted, aad); err != nil || plain != "provider-key" {
		t.Fatalf("serialized keyring could not decrypt legacy ciphertext: %v", err)
	}
}

func TestWithMasterKeyRejectsIDCollision(t *testing.T) {
	value := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	single, err := FromMasterKey(value)
	if err != nil {
		t.Fatal(err)
	}
	old := testKeys(t, single.active, map[string][]byte{single.active: bytes.Repeat([]byte{2}, 32)})
	before, err := EncodeKeyring(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.WithMasterKey(value); err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("conflicting ID accepted or key exposed: %v", err)
	}
	after, err := EncodeKeyring(old)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected collision modified original keyring: %v", err)
	}
}

func TestKeyringFormattingRedactsAllKeys(t *testing.T) {
	value := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	keys, err := FromMasterKey(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
		for _, value := range []any{keys, *keys} {
			if got := fmt.Sprintf(format, value); got != "credential keyring (redacted)" {
				t.Fatalf("format %s was not redacted", format)
			}
		}
	}
}

func TestEncodeAndExtendRejectInvalidKeyrings(t *testing.T) {
	value := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	for _, keys := range []*Keyring{nil, {}, {active: "missing", keys: map[string][]byte{"valid": bytes.Repeat([]byte{7}, 32)}}, {active: "valid", keys: map[string][]byte{"valid": bytes.Repeat([]byte{7}, 32), "invalid": {1}}}} {
		if _, err := EncodeKeyring(keys); err == nil {
			t.Fatal("invalid keyring encoded")
		}
		if _, err := keys.WithMasterKey(value); err == nil {
			t.Fatal("invalid keyring extended")
		}
	}
}

func TestEncodeKeyringReservesFinalNewlineWithinFileLimit(t *testing.T) {
	keys := testKeys(t, "active", map[string][]byte{
		"active": bytes.Repeat([]byte{1}, 32),
		"old":    bytes.Repeat([]byte{2}, 32),
	})
	data, err := EncodeKeyring(keys)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := keys.keys["old"]
	delete(keys.keys, "old")
	largeID := strings.Repeat("x", len("old")+maxKeyringFileSize-1-len(data))
	keys.keys[largeID] = oldKey
	data, err = EncodeKeyring(keys)
	if err != nil || len(data)+1 != maxKeyringFileSize {
		t.Fatalf("maximum file with final newline rejected: size %d, error %v", len(data)+1, err)
	}
	delete(keys.keys, largeID)
	keys.keys[largeID+"x"] = oldKey
	if data, err := EncodeKeyring(keys); err == nil || data != nil || !strings.Contains(err.Error(), "file size limit") {
		t.Fatalf("encoding left no space for final newline: %v", err)
	}
}
