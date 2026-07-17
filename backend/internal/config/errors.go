package config

import "errors"

// ErrConfigNotFound 当 LoadCLIConfig 收到非空路径但文件不存在时返回。
var ErrConfigNotFound = errors.New("config: file not found")
