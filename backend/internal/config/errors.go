package config

import "errors"

var (
	errEmptyTargetLang  = errors.New("config: target_lang must not be empty")
	errNoEnabledBackend = errors.New("config: at least one backend must be enabled")
)

// ErrConfigNotFound 当 Load 收到非空路径但文件不存在时返回。
var ErrConfigNotFound = errors.New("config: file not found")
