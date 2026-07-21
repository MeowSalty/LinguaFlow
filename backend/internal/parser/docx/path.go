package docx

import (
	"fmt"
	"strings"
)

// pathEntry 表示 element_path 栈中的一个条目。
type pathEntry struct {
	tag   string
	count int
}

// pathTracker 管理 element_path 栈，确保同级同名元素路径唯一。
// 路径使用本地标签名（去命名空间前缀），如 body/p[0]、body/tbl[1]/tr[0]/tc[2]/p[0]。
type pathTracker struct {
	stack    []pathEntry
	counters []map[string]int
}

func newPathTracker() *pathTracker {
	return &pathTracker{}
}

// push 将标签推入路径栈，自动计算同级索引。
func (pt *pathTracker) push(tag string) {
	depth := len(pt.stack)
	for len(pt.counters) <= depth {
		pt.counters = append(pt.counters, make(map[string]int))
	}
	idx := pt.counters[depth][tag]
	pt.counters[depth][tag]++
	pt.stack = append(pt.stack, pathEntry{tag: tag, count: idx})
}

// pop 从路径栈弹出栈顶元素，并重置子级计数器。
func (pt *pathTracker) pop() {
	if len(pt.stack) > 0 {
		depth := len(pt.stack) - 1
		pt.stack = pt.stack[:depth]
		childDepth := depth + 1
		if childDepth < len(pt.counters) {
			pt.counters[childDepth] = make(map[string]int)
		}
	}
}

// path 返回当前 element_path 字符串。
func (pt *pathTracker) path() string {
	return buildElementPath(pt.stack)
}

// buildElementPath 根据路径栈生成 DOM 节点路径。
// 例如：body/p[0]、body/tbl[1]/tr[0]/tc[2]/p[0]
func buildElementPath(stack []pathEntry) string {
	var b strings.Builder
	for i, entry := range stack {
		if i > 0 {
			b.WriteByte('/')
		}
		b.WriteString(entry.tag)
		if entry.count > 0 {
			fmt.Fprintf(&b, "[%d]", entry.count)
		}
	}
	return b.String()
}
