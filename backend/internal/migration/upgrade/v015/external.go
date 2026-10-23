package v015

import (
	"context"
	"database/sql"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagenet"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/storeutil"
)

type ExternalReceipt struct {
	Version           int              `json:"version"`
	SourceFingerprint string           `json:"source_fingerprint"`
	Objects           []ExternalObject `json:"objects"`
}

type ExternalObject struct {
	LocationID        int       `json:"location_id"`
	SpaceID           int       `json:"space_id"`
	Key               string    `json:"key"`
	SourceVersion     string    `json:"source_version,omitempty"`
	Size              int64     `json:"size"`
	SHA256            string    `json:"sha256"`
	RecoveryFile      string    `json:"recovery_file,omitempty"`
	ImmutableVersion  string    `json:"immutable_version,omitempty"`
	RetainUntil       time.Time `json:"retain_until"`
	RetentionEvidence string    `json:"retention_evidence"`
}

// VerifyExternalBackup verifies bytes from the exact source and the independent
// recovery material. A declaration or an application GC pin is insufficient.
func VerifyExternalBackup(ctx context.Context, db *sql.DB, cfg config.ServerConfig, keys *credential.Keyring, p *operation.Plan, objects []BackupObject) error {
	remote := []BackupObject{}
	for _, o := range objects {
		if o.Driver == "s3" {
			remote = append(remote, o)
		}
	}
	if len(remote) == 0 {
		if p.ExternalReceipt != "" {
			return operation.Fail(operation.BackupIncomplete, "external receipt has no matching source objects")
		}
		return nil
	}
	if p.ExternalReceipt == "" || !operation.ValidHex(p.ExternalDigest, 64) {
		return operation.Fail(operation.BackupIncomplete, "S3 objects require a fingerprint-bound external recovery receipt")
	}
	proof, err := operation.Hash(ctx, p.ExternalReceipt)
	if err != nil || proof.SHA256 != p.ExternalDigest {
		return operation.Fail(operation.BackupIncomplete, "external receipt bytes changed")
	}
	receipt := new(ExternalReceipt)
	if err := operation.Read(p.ExternalReceipt, receipt); err != nil {
		return err
	}
	if receipt.Version != 1 || receipt.SourceFingerprint != p.SourceFingerprint || len(receipt.Objects) != len(remote) {
		return operation.Fail(operation.BackupIncomplete, "external receipt does not bind the exact source objects")
	}
	seen := map[int]bool{}
	for _, o := range remote {
		var evidence *ExternalObject
		for i := range receipt.Objects {
			candidate := &receipt.Objects[i]
			if candidate.LocationID == o.LocationID {
				if evidence != nil {
					return operation.Fail(operation.BackupIncomplete, "duplicate external object evidence")
				}
				evidence = candidate
			}
		}
		if evidence == nil || seen[o.LocationID] || evidence.SpaceID != o.SpaceID || evidence.Key != o.Key || evidence.SourceVersion != o.Version || evidence.Size < 0 || !operation.ValidHex(evidence.SHA256, 64) || evidence.RetentionEvidence == "" || !evidence.RetainUntil.After(time.Now().UTC()) || (evidence.RecoveryFile == "") == (evidence.ImmutableVersion == "") || o.ExpectedSize != nil && (*o.ExpectedSize != evidence.Size || o.ExpectedSHA256 != evidence.SHA256) {
			return operation.Fail(operation.BackupIncomplete, "external object evidence is incomplete")
		}
		seen[o.LocationID] = true
		client, bucket, prefix, err := sourceS3(ctx, db, cfg, keys, o.SpaceID)
		if err != nil {
			return err
		}
		if err := verifyS3Bytes(ctx, client, bucket, path.Join(prefix, o.Key), o.Version, evidence.Size, evidence.SHA256, cfg.Storage.Limits.MaxFileBytes); err != nil {
			return err
		}
		if err := verifyS3Recovery(ctx, client, bucket, path.Join(prefix, o.Key), cfg, *evidence); err != nil {
			return err
		}
	}
	return nil
}

func verifyS3Recovery(ctx context.Context, client *s3.Client, bucket, key string, cfg config.ServerConfig, evidence ExternalObject) error {
	if evidence.RetentionEvidence == "" || !evidence.RetainUntil.After(time.Now().UTC()) || (evidence.RecoveryFile == "") == (evidence.ImmutableVersion == "") {
		return operation.Fail(operation.BackupIncomplete, "recovery and retention evidence is incomplete")
	}
	if evidence.RecoveryFile != "" {
		for _, backend := range cfg.Storage.Backends {
			if backend.Driver == "local" && operation.Overlap(backend.Root, evidence.RecoveryFile) {
				return operation.Fail(operation.BackupIncomplete, "external recovery material overlaps managed source storage")
			}
		}
		if operation.Overlap(cfg.DataDir, evidence.RecoveryFile) {
			return operation.Fail(operation.BackupIncomplete, "external recovery material overlaps source data directory")
		}
		actual, err := operation.Hash(ctx, evidence.RecoveryFile)
		if err != nil || actual.Size != evidence.Size || actual.SHA256 != evidence.SHA256 {
			return operation.Fail(operation.BackupIncomplete, "independent external recovery bytes are unavailable or changed")
		}
		return nil
	}
	if evidence.ImmutableVersion == "null" {
		return operation.Fail(operation.BackupIncomplete, "S3 null versions are mutable")
	}
	if err := verifyS3Bytes(ctx, client, bucket, key, evidence.ImmutableVersion, evidence.Size, evidence.SHA256, cfg.Storage.Limits.MaxFileBytes); err != nil {
		return err
	}
	retained, err := client.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(evidence.ImmutableVersion)})
	if err != nil || retained.Retention == nil || retained.Retention.Mode != types.ObjectLockRetentionModeCompliance || retained.Retention.RetainUntilDate == nil || retained.Retention.RetainUntilDate.Before(evidence.RetainUntil) {
		return operation.Fail(operation.BackupIncomplete, "S3 immutable version compliance retention could not be verified")
	}
	return nil
}

func verifyS3Bytes(ctx context.Context, client *s3.Client, bucket, key, version string, size int64, digest string, limit int64) error {
	input := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if version != "" {
		input.VersionId = aws.String(version)
	}
	response, err := client.GetObject(ctx, input)
	if err != nil {
		return operation.Fail(operation.BackupIncomplete, "exact S3 object cannot be read")
	}
	defer response.Body.Close()
	if version != "" && aws.ToString(response.VersionId) != version {
		return operation.Fail(operation.BackupIncomplete, "S3 returned a different object version")
	}
	n, hash, err := hashReader(ctx, response.Body, limit)
	if err != nil || n != size || hash != digest {
		return operation.Fail(operation.BackupIncomplete, "exact S3 object bytes differ from the external receipt")
	}
	return nil
}

func sourceS3(ctx context.Context, db *sql.DB, cfg config.ServerConfig, keys *credential.Keyring, spaceID int) (*s3.Client, string, string, error) {
	var id, ownerID int
	var generation int64
	var driver, scope, endpoint, region, bucket, prefix, backend, authSource string
	var pathStyle bool
	query := `SELECT c.id,c.owner_id,c.active_auth_generation,c.driver,c.owner_kind,c.endpoint,c.region,sp.bucket,sp.prefix,COALESCE(c.backend_id,''),c.auth_source,c.path_style FROM storage_connections c JOIN storage_spaces sp ON sp.connection_id=c.id WHERE sp.id=`
	if err := db.QueryRowContext(ctx, query+fmtInt(spaceID)).Scan(&id, &ownerID, &generation, &driver, &scope, &endpoint, &region, &bucket, &prefix, &backend, &authSource, &pathStyle); err != nil || driver != "s3" || storeutil.ValidateKey(strings.TrimSuffix(prefix, "/")) != nil && prefix != "" {
		return nil, "", "", operation.Fail(operation.BackupIncomplete, "source S3 identity is unavailable")
	}
	var payload storageauth.S3Payload
	if authSource == "deployment" {
		for _, b := range cfg.Storage.Backends {
			if b.ID == backend && b.Driver == "s3" && b.Endpoint == endpoint && b.Region == region && b.Bucket == bucket && strings.TrimSuffix(b.Prefix, "/") == strings.TrimSuffix(prefix, "/") {
				payload = storageauth.S3Payload{Version: 1, AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey, SessionToken: b.SessionToken}
			}
		}
	}
	if authSource == "stored" {
		var encrypted credential.Ciphertext
		var aad, payloadVersion int
		encrypted.Version = 1
		if err := db.QueryRowContext(ctx, `SELECT key_id,nonce,ciphertext,aad_version,payload_version FROM storage_auth_versions WHERE connection_id=`+fmtInt(id)+` AND generation=`+fmtInt64(generation)).Scan(&encrypted.KeyID, &encrypted.Nonce, &encrypted.Data, &aad, &payloadVersion); err != nil || aad != 1 || payloadVersion != 1 || keys == nil {
			return nil, "", "", operation.Fail(operation.BackupIncomplete, "source S3 authorization version is unavailable")
		}
		var err error
		payload, err = storageauth.DecryptS3(keys, storageauth.Identity{ConnectionID: id, Scope: scope, OwnerID: ownerID, Driver: driver, Endpoint: endpoint, AuthGeneration: generation}, encrypted)
		if err != nil {
			return nil, "", "", operation.Fail(operation.BackupIncomplete, "source S3 authorization cannot be recovered")
		}
	}
	if payload.Validate() != nil {
		return nil, "", "", operation.Fail(operation.BackupIncomplete, "explicit S3 authorization is required")
	}
	httpClient, err := storagenet.NewClient(endpoint, cfg.Storage.NetworkPolicy(bucket))
	if err != nil {
		return nil, "", "", operation.Fail(operation.BackupIncomplete, "S3 endpoint is outside the deployment network policy")
	}
	options := s3.Options{Region: region, BaseEndpoint: aws.String(endpoint), UsePathStyle: pathStyle, Credentials: credentials.NewStaticCredentialsProvider(payload.AccessKeyID, payload.SecretAccessKey, payload.SessionToken), HTTPClient: httpClient, Retryer: aws.NopRetryer{}, DisableS3ExpressSessionAuth: aws.Bool(true), DisableMultiRegionAccessPoints: true, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}
	return s3.New(options), bucket, prefix, nil
}

func verifySourceStorageKeys(ctx context.Context, db *sql.DB, keys *credential.Keyring) error {
	rows, err := db.QueryContext(ctx, `SELECT v.key_id,v.nonce,v.ciphertext,v.aad_version,v.payload_version,v.generation,c.id,c.owner_kind,c.owner_id,c.driver,c.endpoint FROM storage_auth_versions v JOIN storage_connections c ON c.id=v.connection_id ORDER BY v.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		encrypted := credential.Ciphertext{Version: 1}
		var aad, payload int
		var identity storageauth.Identity
		if err := rows.Scan(&encrypted.KeyID, &encrypted.Nonce, &encrypted.Data, &aad, &payload, &identity.AuthGeneration, &identity.ConnectionID, &identity.Scope, &identity.OwnerID, &identity.Driver, &identity.Endpoint); err != nil {
			return err
		}
		if aad != 1 || payload != 1 || keys == nil {
			return operation.Fail(operation.BackupIncomplete, "source storage encryption keys are unavailable")
		}
		if _, err := storageauth.DecryptS3(keys, identity, encrypted); err != nil {
			return operation.Fail(operation.BackupIncomplete, "source storage credential cannot be recovered")
		}
	}
	return rows.Err()
}

// Helpers format only stable database integer identities, never input SQL.
func fmtInt(n int) string     { return fmtInt64(int64(n)) }
func fmtInt64(n int64) string { return strconv.FormatInt(n, 10) }
