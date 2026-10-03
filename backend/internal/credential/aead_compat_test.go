package credential

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestLegacyLLMAADFixtureSurvivesSharedAEAD(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	k := testKeys(t, "legacy", map[string][]byte{"legacy": key})
	// 这些是提取共享 AEAD 原语之前编码出的确切字节。
	const fixtureAAD = `{"format":1,"id":3,"version":1,"provider":"openai","endpoint":"https://api.openai.com/v1/","scope":"user","owner_id":7}`
	aad := AssociatedData{ID: 3, Version: 1, Provider: "openai", Endpoint: "https://api.openai.com/v1/", Scope: "user", OwnerID: 7}
	if !bytes.Equal(aad.bytes(), []byte(fixtureAAD)) {
		t.Fatal("legacy associated-data bytes changed")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{2}, 12)
	fixture := Ciphertext{Version: 1, KeyID: "legacy", Nonce: nonce, Data: a.Seal(nil, nonce, []byte("legacy-secret"), []byte(fixtureAAD))}
	got, err := k.Decrypt(fixture, aad)
	if err != nil || got != "legacy-secret" {
		t.Fatalf("legacy fixture failed: %v", err)
	}
}
