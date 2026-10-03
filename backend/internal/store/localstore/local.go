// Package localstore 实现被限制在 os.Root 内的不可变对象。
package localstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/storeutil"
)

type Store struct{ root *os.Root }

func New(directory string) (*Store, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, storage.ErrInvalidKey
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, storage.ErrInvalidKey
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, localError(err)
	}
	return OpenExisting(directory)
}

// OpenExisting 打开已存在的根目录，不创建目录。适用于只读清单
// 以及显式的旧版 location 适配器。
func OpenExisting(directory string) (*Store, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, storage.ErrInvalidKey
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, storage.ErrInvalidKey
	}
	for current := directory; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return nil, localError(err)
		}
		if err := ordinary(info); err != nil {
			return nil, err
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, localError(err)
	}
	return &Store{root: root}, nil
}

func (s *Store) Close() error { return s.root.Close() }

func (s *Store) Capabilities(ctx context.Context) (storage.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return storage.Capabilities{}, err
	}
	return storage.Capabilities{ConditionalCreate: true, DirectorySync: directorySyncSupported}, nil
}

// PutNew 使用排他且确定性的暂存同名文件（staging sibling）。其名称由登记的
// key 派生，因此崩溃不会留下无法辨认的临时文件。
// 已存在的暂存文件意味着更早的尝试仍在等待对账。
func (s *Store) PutNew(ctx context.Context, key string, source io.Reader, size int64) (storage.Object, error) {
	if err := storeutil.ValidateKey(key); err != nil {
		return storage.Object{}, err
	}
	reader, err := storeutil.NewExactReader(ctx, source, size)
	if err != nil {
		return storage.Object{}, err
	}
	if err := s.checkPath(key, true); err != nil {
		return storage.Object{}, err
	}
	dir := path.Dir(key)
	if err := s.root.MkdirAll(dir, 0700); err != nil {
		return storage.Object{}, localError(err)
	}
	if err := s.checkPath(key, true); err != nil {
		return storage.Object{}, err
	}
	if _, err := s.root.Lstat(key); err == nil {
		return storage.Object{}, storage.ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return storage.Object{}, localError(err)
	}
	stage := stageKey(key)
	f, err := s.root.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return storage.Object{}, storage.ErrUnavailable
	}
	if err != nil {
		return storage.Object{}, localError(err)
	}
	defer f.Close()
	// 出错时保留已登记的确切暂存文件。Delete 是唯一的清理路径，
	// 且必须等服务层已停止该尝试并完成状态对账之后。
	if err := s.syncParents(dir); err != nil {
		return storage.Object{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, hash), reader); err != nil {
		return storage.Object{}, localError(err)
	}
	if err := reader.Complete(); err != nil {
		return storage.Object{}, err
	}
	if err := f.Sync(); err != nil {
		return storage.Object{}, localError(err)
	}
	if err := ctx.Err(); err != nil {
		return storage.Object{}, err
	}
	// Link 在 Unix 与 Windows 上都是“不存在才创建”语义。若用 Rename，
	// 在 Unix 上会覆盖并发 PutNew 的胜出者。
	if err := s.root.Link(stage, key); err != nil {
		return storage.Object{}, localError(err)
	}
	if err := f.Sync(); err != nil {
		return storage.Object{}, localError(err)
	}
	if err := syncDirectory(s.root, dir); err != nil {
		return storage.Object{}, localError(err)
	}
	if err := f.Close(); err != nil {
		return storage.Object{}, localError(err)
	}
	if err := s.root.Remove(stage); err != nil {
		return storage.Object{}, localError(err)
	}
	if err := syncDirectory(s.root, dir); err != nil {
		return storage.Object{}, localError(err)
	}
	return storage.Object{Key: key, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (s *Store) Open(ctx context.Context, object storage.Object) (io.ReadCloser, error) {
	if err := s.validate(ctx, object); err != nil {
		return nil, err
	}
	f, err := s.root.Open(object.Key)
	if err != nil {
		return nil, s.missingOrPending(object.Key, err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, localError(err)
		}
		return nil, storage.ErrInvalidKey
	}
	return &contextFile{File: f, ctx: ctx}, nil
}

// 为归档解析器保留 ReaderAt/Seeker，避免把本已在本地、大小有界的文件
// 再次物化一遍。
type contextFile struct {
	*os.File
	ctx context.Context
}

func (f *contextFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(p)
}

func (f *contextFile) ReadAt(p []byte, offset int64) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.ReadAt(p, offset)
}

func (f *contextFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Seek(offset, whence)
}

func (f *contextFile) WriteTo(destination io.Writer) (int64, error) {
	// 不要继承 os.File.WriteTo 的优化路径：它会绕过 Read，
	// 否则也会绕过操作的取消检查。
	return io.Copy(destination, struct{ io.Reader }{f})
}

func (s *Store) Stat(ctx context.Context, object storage.Object) (storage.Object, error) {
	if err := s.validate(ctx, object); err != nil {
		return storage.Object{}, err
	}
	info, err := s.root.Lstat(object.Key)
	if err != nil {
		return storage.Object{}, s.missingOrPending(object.Key, err)
	}
	if !info.Mode().IsRegular() {
		return storage.Object{}, storage.ErrInvalidKey
	}
	// Stat 绝不根据先前的 Put 或文件系统元数据编造可信摘要。
	return storage.Object{Key: object.Key, Size: info.Size()}, nil
}

func (s *Store) Delete(ctx context.Context, object storage.Object) error {
	if err := s.validate(ctx, object); err != nil {
		return err
	}
	for _, key := range []string{object.Key, stageKey(object.Key)} {
		info, err := s.root.Lstat(key)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return localError(err)
		}
		if err := ordinary(info); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return storage.ErrInvalidKey
		}
		if err := s.root.Remove(key); err != nil && !errors.Is(err, os.ErrNotExist) {
			return localError(err)
		}
	}
	if err := syncDirectory(s.root, path.Dir(object.Key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return localError(err)
	}
	return nil
}

func (s *Store) validate(ctx context.Context, object storage.Object) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storeutil.ValidateKey(object.Key); err != nil {
		return err
	}
	if object.Version != "" || object.DeleteMarker {
		return storage.ErrUnsupported
	}
	return s.checkPath(object.Key, true)
}

func (s *Store) checkPath(key string, missingOK bool) error {
	parts := strings.Split(key, "/")
	for i := range parts {
		info, err := s.root.Lstat(strings.Join(parts[:i+1], "/"))
		if missingOK && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return localError(err)
		}
		if err := ordinary(info); err != nil {
			return err
		}
		if i < len(parts)-1 && !info.IsDir() {
			return storage.ErrInvalidKey
		}
	}
	return nil
}

func (s *Store) missingOrPending(key string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		if _, stageErr := s.root.Lstat(stageKey(key)); stageErr == nil {
			return storage.ErrUnavailable
		} else if !errors.Is(stageErr, os.ErrNotExist) {
			return localError(stageErr)
		}
	}
	return localError(err)
}

func (s *Store) syncParents(dir string) error {
	for {
		if err := syncDirectory(s.root, dir); err != nil {
			return localError(err)
		}
		if dir == "." {
			return nil
		}
		dir = path.Dir(dir)
	}
}

func stageKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return path.Join(path.Dir(key), ".lf-write-"+hex.EncodeToString(sum[:]))
}

func localError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, storage.ErrLimit), errors.Is(err, storage.ErrCorrupt):
		return err
	case errors.Is(err, os.ErrExist):
		return storage.ErrExists
	case errors.Is(err, os.ErrNotExist):
		return storage.ErrNotFound
	case errors.Is(err, os.ErrPermission):
		return storage.ErrPermission
	default:
		return fmt.Errorf("local object I/O: %w", storage.ErrUnavailable)
	}
}
