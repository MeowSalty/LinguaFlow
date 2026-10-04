// Package storage 定义存储服务与适配器共享的字节级契约。
// 授权、引用与计量归属于服务层。
package storage

import (
	"context"
	"errors"
	"io"
)

var (
	ErrNotFound        = errors.New("source_missing")
	ErrCorrupt         = errors.New("source_corrupt")
	ErrExists          = errors.New("storage_object_exists")
	ErrUnavailable     = errors.New("storage_unavailable")
	ErrPermission      = errors.New("storage_permission_denied")
	ErrAuthRequired    = errors.New("storage_auth_required")
	ErrLimit           = errors.New("storage_quota_exceeded")
	ErrPayloadTooLarge = errors.New("storage_payload_too_large")
	ErrInvalidKey      = errors.New("storage_invalid_key")
	ErrUnsupported     = errors.New("storage_capability_unsupported")
)

// Object 标识一个精确位置。Stat 返回的 Size 与 checksum 只是
// 观测值；只有完整的字节校验才能确立可信身份。
type Object struct {
	Key          string
	Version      string
	Size         int64
	SHA256       string
	DeleteMarker bool
}

// Driver 从不覆盖已有 key。一次失败的写入可能已到达提供方；
// 调用方必须先协调其持久化意图，之后才能尝试另一个 key。
// PutNew 的 size 是精确的预期字节数；输入不足与超出
// 都会失败。Delete 是幂等的，且只针对精确注册的那次尝试。
type Driver interface {
	PutNew(context.Context, string, io.Reader, int64) (Object, error)
	Open(context.Context, Object) (io.ReadCloser, error)
	Stat(context.Context, Object) (Object, error)
	Delete(context.Context, Object) error
}

// VersionLister 是向带版本空间进行写入准入所必需的。它必须
// 只列出精确注册的 key，包括删除标记。
type VersionLister interface {
	Versions(context.Context, string) ([]Object, error)
}

// Capabilities 描述驱动语义与观测到的 bucket 版本化状态。它
// 并不证明授权，也不证明提供方的条件写入行为；
// 准入还必须通过提供方契约与精确 key 探针测试。
type Capabilities struct {
	ConditionalCreate bool
	Versioned         bool
	ExactVersions     bool
	// DirectorySync 报告本地目录发布是否被显式
	// 同步。Windows 目前提供的是文件同步，而非这一更强的保证。
	DirectorySync bool
}

type CapabilityInspector interface {
	Capabilities(context.Context) (Capabilities, error)
}
