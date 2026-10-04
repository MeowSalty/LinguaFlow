package service

import (
	"errors"
	"io"
)

// Close 在请求与执行器停止后释放本地目录句柄。
func (s *StorageService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	for id, d := range s.drivers {
		if closer, ok := d.(io.Closer); ok {
			err = errors.Join(err, closer.Close())
		}
		delete(s.drivers, id)
	}
	return err
}
func (s *ResourceService) Close() error {
	if s.storage == nil {
		return nil
	}
	return s.storage.Close()
}
