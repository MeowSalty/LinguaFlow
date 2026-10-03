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

// Keyring 加载后不可变。密钥轮换的部署方式：先以密钥超集和新的生效 ID 重启，
// 再重新加密已存储的版本。
type Keyring struct {
	active string
	keys   map[string][]byte
}

const maxKeyringFileSize = 1 << 20

func (Keyring) String() string   { return "credential keyring (redacted)" }
func (Keyring) GoString() string { return "credential keyring (redacted)" }

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
	// 校验的是即将读取的已打开文件，而不是对路径的二次查找。
	// 诊断与启动共用这一只读策略，两者都不修复 ACL。
	if err := checkFilePermissions(f); err != nil {
		return nil, fmt.Errorf("credential keyring permissions: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyringFileSize+1))
	if err != nil {
		return nil, errors.New("cannot read credential keyring")
	}
	if len(data) > maxKeyringFileSize {
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

// rejectDuplicateKeys 还会拒绝尾随文档与重复的键。
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

// PrepareKeyring 发布一个完整写入的私有文件，且绝不替换已有的密钥环。
// 调用方必须依据实例状态决定 allowCreate。
func PrepareKeyring(path string, allowCreate bool) (*Keyring, error) {
	k, err := LoadKeyring(path)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !allowCreate {
		return nil, err
	}
	_, data, err := GenerateKeyring()
	if err != nil {
		return nil, err
	}
	if _, err := PublishPrivateFile(path, data); err != nil {
		return nil, err
	}
	return LoadKeyring(path)
}

// GenerateKeyring 生成密钥材料但不发布文件。导入方可借此演练，
// 并在提交数据前原样发布这些字节。
func GenerateKeyring() (*Keyring, []byte, error) {
	value, err := GenerateMasterKey()
	if err != nil {
		return nil, nil, err
	}
	keys, err := FromMasterKey(value)
	if err != nil {
		return nil, nil, err
	}
	data, err := EncodeKeyring(keys)
	return keys, data, err
}

// CreatePrivateDirectory 以独占方式创建私有目录，绝不改动已存在的路径。
// 调用方必须校验其父目录与所有权。
func CreatePrivateDirectory(path string) error {
	if _, err := createPrivateDirectory(path); err != nil {
		return fmt.Errorf("create private directory %q: %w", path, err)
	}
	return nil
}

// PreparePrivateDirectory 在写入敏感内容前创建或收紧工具专属目录的权限。
// 调用方必须校验其父目录与所有权。
func PreparePrivateDirectory(path string) error {
	// 创建目录后的清理错误也可能匹配 ErrExist（目录非空）；
	// 只有真正的创建冲突才允许进入修复流程。
	if created, err := createPrivateDirectory(path); err == nil {
		return nil
	} else if created || !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("prepare private directory %q: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("prepare private directory %q: must be a regular directory", path)
	}
	return restrictDirectory(path)
}

// PublishPrivateFile 原子地发布私有字节，且不覆盖已存在的路径。
// 返回 false 表示并发方或既有所有者已经发布过。
func PublishPrivateFile(path string, data []byte) (published bool, err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, err
	}
	f, err := createPrivateTempFile(dir)
	if err != nil {
		return false, fmt.Errorf("create private file for %q: %w", path, err)
	}
	temp := f.Name()
	defer func() {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
		err = errors.Join(err, os.Remove(temp))
	}()
	if _, err := f.Write(data); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	err = f.Close()
	f = nil
	if err != nil {
		return false, err
	}
	// 硬链接发布是原子操作，若另一启动方抢先则失败。与 Unix 的 rename 不同，
	// 它绝不会覆盖获胜方的密钥材料。
	if err := os.Link(temp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("publish private file %q: %w", path, err)
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

// AssociatedData 带版本号，并使用 JSON 无歧义的字符串编码。
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
