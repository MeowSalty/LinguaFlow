package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
)

type batchPlan struct {
	Ignored []ignoredInput
}

type ignoredInput struct {
	Path   string
	Reason string
}

type inputEntry struct {
	path string
	root string
	rel  string
}

func buildTranslateJobs(inputs []string, output string) ([]FileJob, batchPlan, error) {
	entries, report, err := collectInputEntries(inputs)
	if err != nil {
		return nil, batchPlan{}, err
	}
	if len(entries) == 0 {
		return nil, report, fmt.Errorf("没有可翻译的输入文件")
	}

	singleDirectFile := len(inputs) == 1 && len(entries) == 1 && entries[0].root == ""
	if singleDirectFile {
		if stat, err := os.Stat(output); err == nil && stat.IsDir() {
			return nil, report, fmt.Errorf("单文件输入时 --output/-o 必须是输出文件路径，当前为目录: %s", output)
		}
		return []FileJob{{InputPath: entries[0].path, OutputPath: output}}, report, nil
	}

	if err := os.MkdirAll(output, 0o755); err != nil {
		return nil, report, fmt.Errorf("创建输出目录失败 %s: %w", output, err)
	}
	stat, err := os.Stat(output)
	if err != nil {
		return nil, report, fmt.Errorf("读取输出路径失败 %s: %w", output, err)
	}
	if !stat.IsDir() {
		return nil, report, fmt.Errorf("多文件或目录输入时 --output/-o 必须是输出目录: %s", output)
	}

	jobs := make([]FileJob, 0, len(entries))
	for _, entry := range entries {
		rel := entry.outputRelativePath()
		jobs = append(jobs, FileJob{InputPath: entry.path, OutputPath: filepath.Join(output, rel)})
	}
	return jobs, report, nil
}

func (e inputEntry) outputRelativePath() string {
	if e.root == "" {
		return filepath.Base(e.path)
	}
	return e.rel
}

func collectInputEntries(inputs []string) ([]inputEntry, batchPlan, error) {
	var (
		entries []inputEntry
		report  batchPlan
	)
	seen := map[string]struct{}{}
	for _, raw := range inputs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		cleaned := filepath.Clean(raw)
		info, err := os.Stat(cleaned)
		if err != nil {
			return nil, report, fmt.Errorf("读取输入路径失败 %s: %w", cleaned, err)
		}
		if info.IsDir() {
			dirEntries, ignored, err := collectDirectoryEntries(cleaned)
			if err != nil {
				return nil, report, err
			}
			report.Ignored = append(report.Ignored, ignored...)
			for _, entry := range dirEntries {
				if _, ok := seen[entry.path]; ok {
					continue
				}
				seen[entry.path] = struct{}{}
				entries = append(entries, entry)
			}
			continue
		}
		if err := ensureSupportedFile(cleaned); err != nil {
			if errors.Is(err, parser.ErrNoParser) {
				report.Ignored = append(report.Ignored, ignoredInput{Path: cleaned, Reason: err.Error()})
				continue
			}
			return nil, report, err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		entries = append(entries, inputEntry{path: cleaned})
	}
	return entries, report, nil
}

func collectDirectoryEntries(root string) ([]inputEntry, []ignoredInput, error) {
	var entries []inputEntry
	var ignored []ignoredInput
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if err := ensureSupportedFile(path); err != nil {
			if errors.Is(err, parser.ErrNoParser) {
				ignored = append(ignored, ignoredInput{Path: path, Reason: err.Error()})
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("计算相对路径失败 %s: %w", path, err)
		}
		entries = append(entries, inputEntry{path: path, root: root, rel: rel})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("扫描目录失败 %s: %w", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	sort.Slice(ignored, func(i, j int) bool { return ignored[i].Path < ignored[j].Path })
	return entries, ignored, nil
}

func ensureSupportedFile(path string) error {
	_, err := parser.DetectByExt(path)
	return err
}
