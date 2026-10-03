package storageauth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func keys(t *testing.T) *credential.Keyring {
	t.Helper()
	k, err := credential.ParseKeyring([]byte(`{"version":1,"active_key_id":"one","keys":{"one":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)) + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptionSeparatesIdentityAndDomain(t *testing.T) {
	k := keys(t)
	id := Identity{ConnectionID: 1, Scope: "user", OwnerID: 2, Driver: "s3", Endpoint: "https://s3.example", AuthGeneration: 3}
	payload := S3Payload{Version: 1, AccessKeyID: "access", SecretAccessKey: "secret", SessionToken: "session"}
	a, err := EncryptS3(k, id, payload)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncryptS3(k, id, payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Nonce, b.Nonce) {
		t.Fatal("nonce reused")
	}
	if got, err := DecryptS3(k, id, a); err != nil || got != payload {
		t.Fatalf("roundtrip: %v", err)
	}
	for _, mutate := range []func(*Identity){func(v *Identity) { v.ConnectionID++ }, func(v *Identity) { v.OwnerID++ }, func(v *Identity) { v.Scope = "org" }, func(v *Identity) { v.Endpoint = "https://other.example" }, func(v *Identity) { v.AuthGeneration++ }} {
		wrong := id
		mutate(&wrong)
		if _, err := DecryptS3(k, wrong, a); !errors.Is(err, credential.ErrDecrypt) {
			t.Fatalf("different identity accepted: %v", err)
		}
	}
	legacy := credential.AssociatedData{ID: 1, Version: 3, Scope: "user", OwnerID: 2, Provider: "s3", Endpoint: id.Endpoint}
	if _, err := k.Decrypt(a, legacy); !errors.Is(err, credential.ErrDecrypt) {
		t.Fatal("storage ciphertext accepted as LLM")
	}
	llm, err := k.Encrypt("secret", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptS3(k, id, llm); !errors.Is(err, credential.ErrDecrypt) {
		t.Fatal("LLM ciphertext accepted as storage")
	}
	id.Endpoint = "https://S3.EXAMPLE:443/"
	if _, err := DecryptS3(k, id, a); err != nil {
		t.Fatal("normalized identity changed", err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", payload, payload), "secret") {
		t.Fatal("payload log leaked")
	}
}

func TestStrictPayload(t *testing.T) {
	valid := `{"version":1,"access_key_id":"a","secret_access_key":"s"}`
	if _, err := ParseS3([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{valid + ` {}`, `null`, `{}`, `{"version":1,"access_key_id":"a","secret_access_key":"s","extra":1}`, `{"version":1,"access_key_id":"a","secret_access_key":"s","access_key_id":"b"}`, `{"version":1,"ACCESS_KEY_ID":"a","secret_access_key":"s"}`, `{"version":2,"access_key_id":"a","secret_access_key":"s"}`, `{"version":1,"access_key_id":"a","secret_access_key":"s\n"}`} {
		if _, err := ParseS3([]byte(raw)); err == nil {
			t.Fatalf("invalid payload accepted: %s", raw)
		}
	}
	if _, err := ParseS3([]byte(`{"version":1,"access_key_id":"a","secret_access_key":"s","session_token":null}`)); err == nil {
		t.Fatal("null optional secret accepted")
	}
	if _, err := EncryptS3(nil, Identity{ConnectionID: 1, AuthGeneration: 1, Scope: "user", OwnerID: 1, Driver: "s3", Endpoint: "https://s3.example"}, S3Payload{Version: 1, AccessKeyID: "a", SecretAccessKey: "s"}); !errors.Is(err, credential.ErrKeyUnavailable) {
		t.Fatalf("missing key: %v", err)
	}
}

func TestReencryptUsesNewKeyAndPreservesIdentity(t *testing.T) {
	old := keys(t)
	k, err := credential.ParseKeyring([]byte(`{"version":1,"active_key_id":"two","keys":{"one":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)) + `","two":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)) + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{ConnectionID: 1, Scope: "user", OwnerID: 2, Driver: "s3", Endpoint: "https://s3.example", AuthGeneration: 3}
	a, err := EncryptS3(old, id, S3Payload{Version: 1, AccessKeyID: "a", SecretAccessKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Reencrypt(k, id, a)
	if err != nil {
		t.Fatal(err)
	}
	if b.KeyID != "two" || bytes.Equal(a.Nonce, b.Nonce) {
		t.Fatal("rotation did not use active key and fresh nonce")
	}
	if _, err := DecryptS3(k, id, b); err != nil {
		t.Fatal(err)
	}
}
