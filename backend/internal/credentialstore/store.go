// Package credentialstore persists encrypted credentials in the caller's
// transaction. Authorization and concurrency control belong to the caller.
package credentialstore

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

type CreateInput struct {
	Scope    string
	OwnerID  int
	Provider string
	Endpoint string
	Secret   string
}

// Create writes the credential and its first encrypted version atomically when
// client is a transaction client. It does not open or commit a transaction.
func Create(ctx context.Context, client *ent.Client, keys *credential.Keyring, input CreateInput) (*ent.Credential, error) {
	if client == nil || keys == nil || input.OwnerID <= 0 || (input.Scope != "user" && input.Scope != "org") || input.Secret == "" || (input.Provider != "openai" && input.Provider != "anthropic" && input.Provider != "google") {
		return nil, credential.ErrInvalid
	}
	endpoint, err := credential.NormalizeEndpoint(input.Provider, input.Endpoint)
	if err != nil {
		return nil, err
	}
	row, err := client.Credential.Create().SetScope(input.Scope).SetOwnerID(input.OwnerID).
		SetProvider(input.Provider).SetEndpoint(endpoint).SetCurrentVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := WriteVersion(ctx, client, keys, row, 1, input.Secret); err != nil {
		return nil, err
	}
	return row, nil
}

// AssociatedData binds ciphertext to its persisted identity and ownership.
func AssociatedData(row *ent.Credential, version int) credential.AssociatedData {
	return credential.AssociatedData{ID: row.ID, Version: version, Provider: row.Provider, Endpoint: row.Endpoint, Scope: row.Scope, OwnerID: row.OwnerID}
}

// WriteVersion writes an immutable encrypted version using the same format as
// normal service creation, rotation and decryption.
func WriteVersion(ctx context.Context, client *ent.Client, keys *credential.Keyring, row *ent.Credential, version int, secret string) (*ent.CredentialVersion, error) {
	if client == nil || keys == nil || row == nil || version < 1 || secret == "" {
		return nil, credential.ErrInvalid
	}
	encrypted, err := keys.Encrypt(secret, AssociatedData(row, version))
	if err != nil {
		return nil, err
	}
	return client.CredentialVersion.Create().SetCredentialID(row.ID).SetVersion(version).
		SetEncryptionVersion(encrypted.Version).SetKeyID(encrypted.KeyID).SetNonce(encrypted.Nonce).SetCiphertext(encrypted.Data).Save(ctx)
}
