// Package storageauth 加密结构化的存储授权。它有意只与 LLM
// 凭据共享密码学原语，而不共享其生命周期。
package storageauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagenet"
)

const (
	AADVersion      = 1
	PayloadVersion  = 1
	MaxPayloadBytes = 32 << 10
)

var ErrInvalid = errors.New("invalid storage authorization")

// Identity 必须来自持久化的连接与授权版本。
// 绝不允许由请求自行选择用于解密的身份。
type Identity struct {
	ConnectionID   int
	Scope          string
	OwnerID        int
	Driver         string
	Endpoint       string
	AuthGeneration int64
}

type associatedData struct {
	Format         int    `json:"format"`
	Purpose        string `json:"purpose"`
	ConnectionID   int    `json:"connection_id"`
	Scope          string `json:"scope"`
	OwnerID        int    `json:"owner_id"`
	Driver         string `json:"driver"`
	Endpoint       string `json:"endpoint"`
	AuthGeneration int64  `json:"auth_generation"`
	PayloadVersion int    `json:"payload_version"`
}

// S3Payload 仅包含显式凭据。本包不查询默认的 AWS 提供方链，
// 空 payload 也不代表任何提供方链。
type S3Payload struct {
	Version         int    `json:"version"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
}

func (S3Payload) String() string   { return "storage authorization (redacted)" }
func (S3Payload) GoString() string { return "storage authorization (redacted)" }

func (p S3Payload) Validate() error {
	if p.Version != PayloadVersion || strings.TrimSpace(p.AccessKeyID) == "" || strings.TrimSpace(p.SecretAccessKey) == "" {
		return ErrInvalid
	}
	for _, value := range []string{p.AccessKeyID, p.SecretAccessKey, p.SessionToken} {
		if len(value) > 16<<10 || strings.ContainsAny(value, "\x00\r\n") {
			return ErrInvalid
		}
	}
	return nil
}

// ParseS3 拒绝未知、重复、大小写不一致及尾随字段。使用单个
// 严格对象可避免 JSON 原本令人意外的“后出现的 key 覆盖前者”规则。
func ParseS3(data []byte) (S3Payload, error) {
	if len(data) == 0 || len(data) > MaxPayloadBytes {
		return S3Payload{}, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return S3Payload{}, ErrInvalid
	}
	var p S3Payload
	seen := map[string]bool{}
	for dec.More() {
		token, err = dec.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return S3Payload{}, ErrInvalid
		}
		seen[name] = true
		switch name {
		case "version":
			err = dec.Decode(&p.Version)
		case "access_key_id":
			err = dec.Decode(&p.AccessKeyID)
		case "secret_access_key":
			err = dec.Decode(&p.SecretAccessKey)
		case "session_token":
			value, tokenErr := dec.Token()
			var valid bool
			p.SessionToken, valid = value.(string)
			if tokenErr != nil || !valid {
				return S3Payload{}, ErrInvalid
			}
		default:
			return S3Payload{}, ErrInvalid
		}
		if err != nil {
			return S3Payload{}, ErrInvalid
		}
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return S3Payload{}, ErrInvalid
	}
	if _, err = dec.Token(); err != io.EOF {
		return S3Payload{}, ErrInvalid
	}
	if err = p.Validate(); err != nil {
		return S3Payload{}, err
	}
	return p, nil
}

func (id Identity) aad() ([]byte, error) {
	if id.ConnectionID <= 0 || id.AuthGeneration <= 0 || id.Driver != "s3" {
		return nil, ErrInvalid
	}
	if (id.Scope != "site" && id.Scope != "user" && id.Scope != "org") || (id.Scope == "site" && id.OwnerID != 0) || (id.Scope != "site" && id.OwnerID <= 0) {
		return nil, ErrInvalid
	}
	endpoint, err := storagenet.NormalizeEndpoint(id.Endpoint)
	if err != nil {
		return nil, ErrInvalid
	}
	return json.Marshal(associatedData{AADVersion, "storage_auth", id.ConnectionID, id.Scope, id.OwnerID, id.Driver, endpoint, id.AuthGeneration, PayloadVersion})
}

func EncryptS3(keys *credential.Keyring, identity Identity, payload S3Payload) (credential.Ciphertext, error) {
	if err := payload.Validate(); err != nil {
		return credential.Ciphertext{}, err
	}
	aad, err := identity.aad()
	if err != nil {
		return credential.Ciphertext{}, err
	}
	plain, err := json.Marshal(payload)
	if err != nil || len(plain) > MaxPayloadBytes {
		return credential.Ciphertext{}, ErrInvalid
	}
	defer clear(plain)
	return keys.Seal(plain, aad)
}

func DecryptS3(keys *credential.Keyring, identity Identity, encrypted credential.Ciphertext) (S3Payload, error) {
	aad, err := identity.aad()
	if err != nil {
		return S3Payload{}, err
	}
	plain, err := keys.Open(encrypted, aad)
	if err != nil {
		return S3Payload{}, err
	}
	defer clear(plain)
	return ParseS3(plain)
}

// Reencrypt 保持所有域身份不变；调用方对旧的
// ciphertext/key ID 使用数据库 CAS，以避免覆盖并发的授权。
func Reencrypt(keys *credential.Keyring, identity Identity, encrypted credential.Ciphertext) (credential.Ciphertext, error) {
	plain, err := DecryptS3(keys, identity, encrypted)
	if err != nil {
		return credential.Ciphertext{}, err
	}
	return EncryptS3(keys, identity, plain)
}
