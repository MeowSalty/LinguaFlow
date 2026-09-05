//go:build linux

package sysmem

import (
	"errors"
	"fmt"
	"os"
)

// readRSS 读取当前进程 RSS：优先解析 /proc/self/status 的 VmRSS 行，
// status 缺失或解析失败时回退到 /proc/self/statm（resident 页数 × 页大小）。
func readRSS() (uint64, error) {
	rss, statusErr := readStatusRSS()
	if statusErr == nil {
		return rss, nil
	}
	rss, statmErr := readStatmRSS()
	if statmErr == nil {
		return rss, nil
	}
	return 0, fmt.Errorf("sysmem: 读取进程 RSS 失败: %w", errors.Join(statusErr, statmErr))
}

// readStatusRSS 读取并解析 /proc/self/status 的 VmRSS 行。
func readStatusRSS() (uint64, error) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, fmt.Errorf("sysmem: 读取 /proc/self/status: %w", err)
	}
	rss, err := parseProcStatusRSS(data)
	if err != nil {
		return 0, err
	}
	return rss, nil
}

// readStatmRSS 读取并解析 /proc/self/statm 的 resident 字段。
func readStatmRSS() (uint64, error) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, fmt.Errorf("sysmem: 读取 /proc/self/statm: %w", err)
	}
	rss, err := parseStatmRSS(data, os.Getpagesize())
	if err != nil {
		return 0, err
	}
	return rss, nil
}
