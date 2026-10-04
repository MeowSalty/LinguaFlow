//go:build windows

package localstore

import (
	"os"
	"syscall"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

const directorySyncSupported = false

func ordinary(info os.FileInfo) error {
	if !info.Mode().IsRegular() && !info.IsDir() {
		return storage.ErrInvalidKey
	}
	if native, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && native.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return storage.ErrInvalidKey
	}
	return nil
}

// Windows 没有目录级的 FlushFileBuffers 等价物。发布流程使用"不存在才创建"
// 的硬链接、NTFS 元数据日志，以及在链接创建前后对文件执行 FlushFileBuffers。
// DirectorySync 保持 false：目录项在断电下的持久性尚未验证，NTFS 也不例外。
func syncDirectory(_ *os.Root, _ string) error { return nil }
