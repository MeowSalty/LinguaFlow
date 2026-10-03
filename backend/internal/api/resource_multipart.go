package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

type resourceUploadPart struct {
	Filename string
	Size     int64
	Header   textproto.MIMEHeader
	file     *service.StorageFile
}

func (p *resourceUploadPart) Open() (io.ReadCloser, error) {
	return io.NopCloser(io.NewSectionReader(p.file, 0, p.Size)), nil
}

type resourceMultipart struct {
	File  map[string][]*resourceUploadPart
	Value map[string][]string
}

type uploadContextReader struct {
	ctx context.Context
	io.Reader
}

func (r uploadContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

// parseResourceMultipart 使用存储的临时文件预算与工作目录。
// 无论何种原因退出都会关闭已打开的文件，包括请求体畸形和客户端取消的情况。
func (s *Server) parseResourceMultipart(w http.ResponseWriter, r *http.Request, maxFiles int) (*resourceMultipart, func(), error) {
	ctx, limits, release, err := s.resourceSvc.BeginUpload(r.Context())
	if err != nil {
		return nil, nil, err
	}
	*r = *r.WithContext(ctx)
	controller := http.NewResponseController(w)
	if deadline, ok := ctx.Deadline(); ok {
		_ = controller.SetReadDeadline(deadline)
		defer controller.SetReadDeadline(time.Time{})
	}
	form := &resourceMultipart{File: map[string][]*resourceUploadPart{}, Value: map[string][]string{}}
	cleanup := func() {
		for _, files := range form.File {
			for _, f := range files {
				_ = f.file.Close()
			}
		}
		release()
	}
	success := false
	defer func() {
		if !success {
			cleanup()
		}
	}()
	// 在限制整批总量的同时，为内层传输暂存（spool）留出余量。
	totalLimit := min(limits.MaxTempBytes/2, limits.MaxFileBytes*int64(maxFiles)+(1<<20))
	if r.ContentLength > totalLimit {
		return nil, nil, service.ErrStorageTooLarge
	}
	r.Body = http.MaxBytesReader(w, r.Body, totalLimit)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nil, err
	}
	var count, fields int
	var values int64
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		name := part.FormName()
		if name == "" {
			_ = part.Close()
			return nil, nil, service.ErrInvalidInput
		}
		if part.FileName() == "" {
			fields++
			if fields > 100 {
				_ = part.Close()
				return nil, nil, service.ErrStorageTooLarge
			}
			data, err := io.ReadAll(io.LimitReader(uploadContextReader{ctx, part}, (1<<20)-values+1))
			_ = part.Close()
			if err != nil {
				return nil, nil, err
			}
			values += int64(len(data))
			if values > 1<<20 {
				return nil, nil, service.ErrStorageTooLarge
			}
			form.Value[name] = append(form.Value[name], string(data))
			continue
		}
		count++
		if count > maxFiles {
			_ = part.Close()
			return nil, nil, service.ErrStorageTooLarge
		}
		f, err := s.resourceSvc.UploadBuffer(limits.MaxFileBytes)
		if err != nil {
			_ = part.Close()
			return nil, nil, err
		}
		candidate := &resourceUploadPart{Filename: part.FileName(), Header: part.Header, file: f}
		form.File[name] = append(form.File[name], candidate)
		n, err := io.Copy(f, io.LimitReader(uploadContextReader{ctx, part}, limits.MaxFileBytes+1))
		_ = part.Close()
		if err != nil {
			return nil, nil, err
		}
		if n > limits.MaxFileBytes {
			return nil, nil, service.ErrStorageTooLarge
		}
		if err = f.Seal(n); err != nil {
			return nil, nil, err
		}
		candidate.Size = n
	}
	r.Form = url.Values(form.Value)
	r.PostForm = r.Form
	r.MultipartForm = &multipart.Form{Value: form.Value, File: map[string][]*multipart.FileHeader{}}
	for name, files := range form.File {
		for _, f := range files {
			r.MultipartForm.File[name] = append(r.MultipartForm.File[name], &multipart.FileHeader{Filename: f.Filename, Size: f.Size})
		}
	}
	success = true
	return form, cleanup, nil
}

func (s *Server) writeResourceMultipartError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		err = service.ErrStorageTooLarge
	}
	if errors.Is(err, service.ErrStorageTooLarge) || errors.Is(err, service.ErrStorageMaintenance) || errors.Is(err, storage.ErrLimit) || errors.Is(err, context.DeadlineExceeded) {
		s.writeStorageError(w, r, err)
		return
	}
	s.writeProblem(w, r, http.StatusBadRequest, "invalid_multipart", "上传表单解析失败")
}

func resourceUploadKey(key string, index int) string {
	if key == "" {
		return ""
	}
	return fmt.Sprintf("%s/%d", key, index)
}
