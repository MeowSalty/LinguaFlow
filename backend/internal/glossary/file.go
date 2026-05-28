package glossary

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// csvHeader 是 FileGlossary 写出的固定首行。Load 时若首行与之大小写无关相等则跳过。
var csvHeader = []string{"source", "target", "case_sensitive", "notes"}

// FileGlossary 是 CSV 文件后端的术语表。
//
// 内存模型：entries 是按 len(Source) 降序排序的副本，Lookup 顺序遍历即可让长术语
// 优先（"Gemini API" 在 "Gemini" 之前命中）；bySource 用规范化的 source 串
// （CaseSensitive=false 时小写）作 key，做 O(1) 去重。
//
// 并发：Lookup 持读锁；Add/Save 持写/读锁。Save 走 os.CreateTemp + os.Rename 原子写。
type FileGlossary struct {
	path     string
	mu       sync.RWMutex
	entries  []Entry
	bySource map[string]int // 规范化 source → entries 下标
	dirty    bool           // Add 过且尚未 Save
}

// NewMemory 返回一个没有 path 的空 FileGlossary。
// 适合测试或者纯内存场景；Save 会因 path 为空而失败。
func NewMemory() *FileGlossary {
	return &FileGlossary{bySource: map[string]int{}}
}

// LoadFile 从 path 读取 CSV；文件不存在时返回空表（首次 bootstrap 场景）。
func LoadFile(path string) (*FileGlossary, error) {
	g := &FileGlossary{
		path:     path,
		bySource: map[string]int{},
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return g, nil
		}
		return nil, fmt.Errorf("glossary: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // 允许变长，自己校验
	r.TrimLeadingSpace = true

	first := true
	logger := slog.Default()
	for lineNo := 1; ; lineNo++ {
		rec, rerr := r.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("glossary: parse %s line %d: %w", path, lineNo, rerr)
		}
		if first {
			first = false
			if isHeader(rec) {
				continue
			}
		}
		if len(rec) < 2 {
			logger.Warn("glossary: skip short row", "path", path, "line", lineNo, "fields", len(rec))
			continue
		}
		e := Entry{
			Source: strings.TrimSpace(rec[0]),
			Target: strings.TrimSpace(rec[1]),
		}
		if e.Source == "" || e.Target == "" {
			logger.Warn("glossary: skip empty source/target", "path", path, "line", lineNo)
			continue
		}
		if len(rec) >= 3 {
			e.CaseSensitive = parseBool(rec[2])
		}
		if len(rec) >= 4 {
			e.Notes = strings.TrimSpace(rec[3])
		}
		g.addLocked(e)
	}
	g.sortLocked()
	g.dirty = false
	return g, nil
}

// Lookup 在 text 中查找命中的术语；忽略语言（MVP 单语言对一表）。
func (g *FileGlossary) Lookup(_ context.Context, text, _, _ string) ([]Entry, error) {
	if text == "" {
		return nil, nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	lowerText := strings.ToLower(text)
	var hits []Entry
	for _, e := range g.entries {
		if e.CaseSensitive {
			if strings.Contains(text, e.Source) {
				hits = append(hits, e)
			}
		} else {
			if strings.Contains(lowerText, strings.ToLower(e.Source)) {
				hits = append(hits, e)
			}
		}
	}
	return hits, nil
}

// Add 严格合并：source 已存在则跳过，不覆盖人工修订；新条目追加。
//
// 返回 AddResult 详述处理结果：成功写入的条目放入 Added；source 已存在且 target
// 不同的写入 Skipped（Reason=exists, Existing 填表中已有版本），调用方可据此做下游
// 修正（例如把本批译文里的 Proposed.Target 替换为 Existing.Target）。
// Proposed.Target 与 Existing.Target 完全相等时视作 noop，既不进 Added 也不进 Skipped。
func (g *FileGlossary) Add(_ context.Context, entries ...Entry) (AddResult, error) {
	var result AddResult
	if len(entries) == 0 {
		return result, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	added := false
	for _, e := range entries {
		e.Source = strings.TrimSpace(e.Source)
		e.Target = strings.TrimSpace(e.Target)
		e.Notes = strings.TrimSpace(e.Notes)
		if e.Source == "" || e.Target == "" {
			result.Skipped = append(result.Skipped, SkippedEntry{Proposed: e, Reason: SkipReasonEmpty})
			continue
		}
		if idx, exists := g.bySource[normKey(e)]; exists {
			existing := g.entries[idx]
			if existing.Target == e.Target {
				// 完全相同的提议视作 noop，不报冲突。
				continue
			}
			result.Skipped = append(result.Skipped, SkippedEntry{
				Proposed: e,
				Existing: existing,
				Reason:   SkipReasonExists,
			})
			continue
		}
		g.addLocked(e)
		result.Added = append(result.Added, e)
		added = true
	}
	if added {
		g.sortLocked()
		g.dirty = true
	}
	return result, nil
}

// Save 原子写出当前所有 entries 到 path。表头固定为 csvHeader。
func (g *FileGlossary) Save(_ context.Context) error {
	g.mu.RLock()
	snapshot := make([]Entry, len(g.entries))
	copy(snapshot, g.entries)
	path := g.path
	g.mu.RUnlock()

	if path == "" {
		return errors.New("glossary: empty path")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("glossary: mkdir %s: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".glossary.*.csv")
	if err != nil {
		return fmt.Errorf("glossary: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	removeTmp := func() { _ = os.Remove(tmpPath) }

	w := csv.NewWriter(tmp)
	if err := w.Write(csvHeader); err != nil {
		_ = tmp.Close()
		removeTmp()
		return fmt.Errorf("glossary: write header: %w", err)
	}
	for _, e := range snapshot {
		rec := []string{e.Source, e.Target, boolStr(e.CaseSensitive), e.Notes}
		if err := w.Write(rec); err != nil {
			_ = tmp.Close()
			removeTmp()
			return fmt.Errorf("glossary: write row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = tmp.Close()
		removeTmp()
		return fmt.Errorf("glossary: flush: %w", err)
	}
	if err := tmp.Close(); err != nil {
		removeTmp()
		return fmt.Errorf("glossary: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		removeTmp()
		return fmt.Errorf("glossary: rename %s -> %s: %w", tmpPath, path, err)
	}
	g.mu.Lock()
	g.dirty = false
	g.mu.Unlock()
	return nil
}

// Path 返回当前文件路径，便于诊断/测试。
func (g *FileGlossary) Path() string { return g.path }

// Len 返回 entries 数量。
func (g *FileGlossary) Len() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.entries)
}

// Dirty 表示自上次 Save 后是否有过 Add；engine 可据此决定是否调 Save。
func (g *FileGlossary) Dirty() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.dirty
}

// SnapshotSources 返回 entries 中 source 列表的副本，按当前内部顺序（长术语优先）。
// 主要用于测试与诊断；副本不会被后续修改影响。
func (g *FileGlossary) SnapshotSources() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, len(g.entries))
	for i, e := range g.entries {
		out[i] = e.Source
	}
	return out
}

// addLocked 追加一条（不排序、不去重、不上锁）。调用方须已持锁且保证唯一性。
func (g *FileGlossary) addLocked(e Entry) {
	g.entries = append(g.entries, e)
	g.bySource[normKey(e)] = len(g.entries) - 1
}

// sortLocked 按 len(Source) 降序重排 entries 并重建 bySource 索引。
func (g *FileGlossary) sortLocked() {
	sort.SliceStable(g.entries, func(i, j int) bool {
		return len(g.entries[i].Source) > len(g.entries[j].Source)
	})
	g.bySource = make(map[string]int, len(g.entries))
	for i, e := range g.entries {
		g.bySource[normKey(e)] = i
	}
}

// normKey 规范化用于查重的 key：CaseSensitive=false 时统一小写。
// 同一 source 同时存在 case_sensitive=true 与 false 两条会按 case_sensitive=true 保留，
// 不区分大小写那条因 lower(source) 与之冲突可能不同——按规范化结果决定。
func normKey(e Entry) string {
	if e.CaseSensitive {
		return "S:" + e.Source
	}
	return "I:" + strings.ToLower(e.Source)
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func isHeader(rec []string) bool {
	if len(rec) < 2 {
		return false
	}
	// 只要前两列分别是 source、target（大小写无关）就视作表头
	return strings.EqualFold(strings.TrimSpace(rec[0]), "source") &&
		strings.EqualFold(strings.TrimSpace(rec[1]), "target")
}
