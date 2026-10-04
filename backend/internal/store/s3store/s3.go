// Package s3store 基于 AWS SDK v2 实现不可变对象契约。
// 网络目的地策略与授权由注入的服务层负责。
package s3store

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/storeutil"
)

type Options struct {
	Endpoint    string
	Region      string
	Bucket      string
	Prefix      string
	PathStyle   bool
	Credentials aws.CredentialsProvider
	HTTPClient  s3.HTTPClient
	// MaxVersions 限制对账时的内存占用，删除标记（delete marker）也计算在内。
	MaxVersions int
}

type Store struct {
	client      *s3.Client
	bucket      string
	prefix      string
	maxVersions int
}

func New(options Options) (*Store, error) {
	if options.Credentials == nil {
		return nil, storage.ErrAuthRequired
	}
	if strings.TrimSpace(options.Bucket) == "" || strings.ContainsAny(options.Bucket, "/\\:@?#") || strings.TrimSpace(options.Region) == "" {
		return nil, storage.ErrInvalidKey
	}
	if options.Endpoint != "" {
		endpoint, err := url.Parse(options.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
			return nil, storage.ErrInvalidKey
		}
	}
	if options.Prefix != "" {
		if err := storeutil.ValidateKey(options.Prefix); err != nil {
			return nil, err
		}
	}
	if options.MaxVersions == 0 {
		options.MaxVersions = 10000
	}
	if options.MaxVersions < 1 {
		return nil, storage.ErrLimit
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	// 绝不加载共享配置、环境变量凭据、实例角色或默认凭据链。
	// 每次操作都使用显式绑定的授权。
	config := s3.Options{
		Region: options.Region, Credentials: explicitCredentials{provider: options.Credentials},
		HTTPClient: client, UsePathStyle: options.PathStyle, Retryer: aws.NopRetryer{},
		DisableS3ExpressSessionAuth: aws.Bool(true), DisableMultiRegionAccessPoints: true,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	if options.Endpoint != "" {
		config.BaseEndpoint = aws.String(options.Endpoint)
	}
	return &Store{client: s3.New(config), bucket: options.Bucket, prefix: options.Prefix, maxVersions: options.MaxVersions}, nil
}

type explicitCredentials struct{ provider aws.CredentialsProvider }

func (p explicitCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) {
	credentials, err := p.provider.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, storage.ErrAuthRequired
	}
	if credentials.AccessKeyID == "" || credentials.SecretAccessKey == "" {
		return aws.Credentials{}, storage.ErrAuthRequired
	}
	return credentials, nil
}

func (s *Store) Capabilities(ctx context.Context) (storage.Capabilities, error) {
	result, err := s.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return storage.Capabilities{}, mapError(err)
	}
	versioned := result.Status == types.BucketVersioningStatusEnabled || result.Status == types.BucketVersioningStatusSuspended
	return storage.Capabilities{ConditionalCreate: true, Versioned: versioned, ExactVersions: true}, nil
}

func (s *Store) PutNew(ctx context.Context, key string, source io.Reader, size int64) (storage.Object, error) {
	fullKey, err := s.key(key)
	if err != nil {
		return storage.Object{}, err
	}
	reader, err := storeutil.NewExactReader(ctx, source, size)
	if err != nil {
		return storage.Object{}, err
	}
	if size == 0 {
		if err := reader.Complete(); err != nil {
			return storage.Object{}, err
		}
	}
	result, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(fullKey), Body: reader,
		ContentLength: aws.Int64(size), IfNoneMatch: aws.String("*"),
	}, func(options *s3.Options) {
		options.APIOptions = append(options.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
	})
	if err != nil {
		if check := reader.Complete(); errors.Is(check, storage.ErrPayloadTooLarge) || errors.Is(check, context.Canceled) || errors.Is(check, context.DeadlineExceeded) {
			return storage.Object{}, check
		}
		return storage.Object{}, mapError(err)
	}
	if err := reader.Complete(); err != nil {
		return storage.Object{}, err
	}
	// 提供方的校验和与 ETag 只是观测值。服务层会独立校验全部字节，
	// 之后才确立 Blob 的可信摘要。
	return storage.Object{Key: key, Version: aws.ToString(result.VersionId), Size: size}, nil
}

func (s *Store) Open(ctx context.Context, object storage.Object) (io.ReadCloser, error) {
	key, err := s.key(object.Key)
	if err != nil {
		return nil, err
	}
	if object.DeleteMarker {
		return nil, storage.ErrNotFound
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), VersionId: versionPointer(object.Version)})
	if err != nil {
		return nil, mapError(err)
	}
	if aws.ToBool(result.DeleteMarker) {
		result.Body.Close()
		return nil, storage.ErrNotFound
	}
	if object.Version != "" && aws.ToString(result.VersionId) != object.Version {
		result.Body.Close()
		return nil, storage.ErrCorrupt
	}
	return &storeutil.ContextReadCloser{Context: ctx, ReadCloser: result.Body}, nil
}

func (s *Store) Stat(ctx context.Context, object storage.Object) (storage.Object, error) {
	key, err := s.key(object.Key)
	if err != nil {
		return storage.Object{}, err
	}
	if object.DeleteMarker {
		versions, err := s.Versions(ctx, object.Key)
		if err != nil {
			return storage.Object{}, err
		}
		for _, version := range versions {
			if version.Version == object.Version && version.DeleteMarker {
				return version, nil
			}
		}
		return storage.Object{}, storage.ErrNotFound
	}
	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), VersionId: versionPointer(object.Version)})
	if err != nil {
		var response *smithyhttp.ResponseError
		if errors.As(err, &response) && response.HTTPStatusCode() == http.StatusNotFound {
			return storage.Object{}, s.confirmMissing(ctx, object)
		}
		return storage.Object{}, mapError(err)
	}
	if object.Version != "" && aws.ToString(result.VersionId) != object.Version {
		return storage.Object{}, storage.ErrCorrupt
	}
	return storage.Object{Key: object.Key, Version: aws.ToString(result.VersionId), Size: aws.ToInt64(result.ContentLength), DeleteMarker: aws.ToBool(result.DeleteMarker)}, nil
}

// HEAD 404 没有错误响应体，可能掩盖访问失败。只有经授权的精确 key
// 列举才能确认对象确实不存在。
func (s *Store) confirmMissing(ctx context.Context, object storage.Object) error {
	if object.Version != "" {
		versions, err := s.Versions(ctx, object.Key)
		if err != nil {
			return err
		}
		for _, version := range versions {
			if version.Version == object.Version {
				return storage.ErrUnavailable
			}
		}
		return storage.ErrNotFound
	}
	key, _ := s.key(object.Key)
	result, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(key), MaxKeys: aws.Int32(1)})
	if err != nil {
		return mapError(err)
	}
	for _, object := range result.Contents {
		if aws.ToString(object.Key) == key {
			return storage.ErrUnavailable
		}
	}
	return storage.ErrNotFound
}

func (s *Store) Delete(ctx context.Context, object storage.Object) error {
	key, err := s.key(object.Key)
	if err != nil {
		return err
	}
	if object.Version == "" {
		capabilities, err := s.Capabilities(ctx)
		if err != nil {
			return err
		}
		// 在已启用/暂停版本控制的存储桶中，裸 DELETE 只会添加删除标记，
		// 所有物理字节都仍会保留。必须改为指定确切版本。
		if capabilities.Versioned || object.DeleteMarker {
			return storage.ErrUnsupported
		}
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), VersionId: versionPointer(object.Version)})
	err = mapError(err)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Store) Versions(ctx context.Context, key string) ([]storage.Object, error) {
	fullKey, err := s.key(key)
	if err != nil {
		return nil, err
	}
	input := &s3.ListObjectVersionsInput{Bucket: aws.String(s.bucket), Prefix: aws.String(fullKey), MaxKeys: aws.Int32(1000)}
	var objects []storage.Object
	for pages := 0; pages < 100; pages++ {
		result, err := s.client.ListObjectVersions(ctx, input)
		if err != nil {
			return nil, mapError(err)
		}
		for _, version := range result.Versions {
			if aws.ToString(version.Key) == fullKey {
				objects = append(objects, storage.Object{Key: key, Version: aws.ToString(version.VersionId), Size: aws.ToInt64(version.Size)})
			}
		}
		for _, marker := range result.DeleteMarkers {
			if aws.ToString(marker.Key) == fullKey {
				objects = append(objects, storage.Object{Key: key, Version: aws.ToString(marker.VersionId), DeleteMarker: true})
			}
		}
		if len(objects) > s.maxVersions {
			return nil, storage.ErrLimit
		}
		if !aws.ToBool(result.IsTruncated) {
			return objects, nil
		}
		if result.NextKeyMarker == nil || (aws.ToString(input.KeyMarker) == aws.ToString(result.NextKeyMarker) && aws.ToString(input.VersionIdMarker) == aws.ToString(result.NextVersionIdMarker)) {
			return nil, storage.ErrUnavailable
		}
		if aws.ToString(result.NextKeyMarker) > fullKey {
			return objects, nil
		}
		input.KeyMarker = result.NextKeyMarker
		input.VersionIdMarker = result.NextVersionIdMarker
	}
	return nil, storage.ErrLimit
}

func (s *Store) key(key string) (string, error) {
	if err := storeutil.ValidateKey(key); err != nil {
		return "", err
	}
	if s.prefix == "" {
		return key, nil
	}
	full := s.prefix + "/" + key
	if len(full) > 1024 {
		return "", storage.ErrInvalidKey
	}
	return full, nil
}

func versionPointer(version string) *string {
	if version == "" {
		return nil
	}
	return aws.String(version)
}

// 提供方的原始诊断信息可能包含端点、请求或凭据细节。
// 只对外暴露稳定的领域错误码；取消信号原样保留给调用方。
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, storage.ErrAuthRequired) {
		return storage.ErrAuthRequired
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NoSuchVersion":
			return storage.ErrNotFound
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken", "TokenRefreshRequired":
			return storage.ErrAuthRequired
		case "AccessDenied", "AllAccessDisabled":
			return storage.ErrPermission
		case "PreconditionFailed", "ConditionalRequestConflict":
			return storage.ErrExists
		case "EntityTooLarge":
			return storage.ErrPayloadTooLarge
		case "QuotaExceeded", "InsufficientStorage":
			return storage.ErrLimit
		case "BadDigest", "IncompleteBody":
			return storage.ErrCorrupt
		case "NotImplemented":
			return storage.ErrUnsupported
		}
	}
	var response *smithyhttp.ResponseError
	if errors.As(err, &response) {
		switch response.HTTPStatusCode() {
		case http.StatusUnauthorized:
			return storage.ErrAuthRequired
		case http.StatusForbidden:
			return storage.ErrPermission
		case http.StatusPreconditionFailed:
			return storage.ErrExists
		case http.StatusRequestEntityTooLarge:
			return storage.ErrPayloadTooLarge
		case http.StatusInsufficientStorage:
			return storage.ErrLimit
		}
		if response.Response != nil && response.Response.Response != nil {
			if delay := parseRetryAfter(response.Response.Header.Get("Retry-After"), time.Now()); delay > 0 {
				return &storage.RetryAfterError{Delay: delay}
			}
		}
	}
	return storage.ErrUnavailable
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 128 {
		return 0
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		const maxDelay = time.Duration(1<<63 - 1)
		if seconds > uint64(maxDelay/time.Second) {
			return maxDelay
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}
