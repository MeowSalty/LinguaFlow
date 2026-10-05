package storagebackup

import (
	"context"
	"errors"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credentialstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

// VerifyKeys 证明已备份的密钥字节能解密所有保留的版本；
// 仅有匹配的 key ID 并不构成可恢复性的证据。
func VerifyKeys(ctx context.Context, client *ent.Client, keys *credential.Keyring) error {
	versions, err := client.CredentialVersion.Query().WithCredential().All(ctx)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if version.Edges.Credential == nil {
			return errors.New("backup credential identity is missing")
		}
		_, err := keys.Decrypt(credential.Ciphertext{Version: version.EncryptionVersion, KeyID: version.KeyID, Nonce: version.Nonce, Data: version.Ciphertext}, credentialstore.AssociatedData(version.Edges.Credential, version.Version))
		if err != nil {
			return errors.New("backup keyring cannot decrypt a preserved LLM credential")
		}
	}
	cloud, err := client.StorageAuthVersion.Query().All(ctx)
	if err != nil {
		return err
	}
	for _, version := range cloud {
		connection, err := client.StorageConnection.Get(ctx, version.ConnectionID)
		if err != nil {
			return err
		}
		if version.AadVersion != storageauth.AADVersion || version.PayloadVersion != storageauth.PayloadVersion {
			return errors.New("backup contains an unsupported storage credential format")
		}
		identity := storageauth.Identity{ConnectionID: connection.ID, Scope: string(connection.OwnerKind), OwnerID: connection.OwnerID, Driver: string(connection.Driver), Endpoint: connection.Endpoint, AuthGeneration: version.Generation}
		_, err = storageauth.DecryptS3(keys, identity, credential.Ciphertext{Version: 1, KeyID: version.KeyID, Nonce: version.Nonce, Data: version.Ciphertext})
		if err != nil {
			return errors.New("backup keyring cannot decrypt a preserved storage credential")
		}
	}
	return nil
}
