package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildTranslateJobsSingleFile(t *testing.T) {
	tmp := t.TempDir()
	input := filepath.Join(tmp, "input.md")
	output := filepath.Join(tmp, "output.md")
	writeTestFile(t, input)

	jobs, report, err := buildTranslateJobs([]string{input}, output)
	if err != nil {
		t.Fatalf("buildTranslateJobs() error = %v", err)
	}
	if len(report.Ignored) != 0 {
		t.Fatalf("ignored = %d, want 0", len(report.Ignored))
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	if jobs[0].InputPath != input {
		t.Fatalf("InputPath = %q, want %q", jobs[0].InputPath, input)
	}
	if jobs[0].OutputPath != output {
		t.Fatalf("OutputPath = %q, want %q", jobs[0].OutputPath, output)
	}
}

func TestBuildTranslateJobsMultiInputsRequireOutputDir(t *testing.T) {
	tmp := t.TempDir()
	inputA := filepath.Join(tmp, "a.md")
	inputB := filepath.Join(tmp, "b.txt")
	outputFile := filepath.Join(tmp, "output.md")
	writeTestFile(t, inputA)
	writeTestFile(t, inputB)
	writeTestFile(t, outputFile)

	_, _, err := buildTranslateJobs([]string{inputA, inputB}, outputFile)
	if err == nil {
		t.Fatal("buildTranslateJobs() error = nil, want error")
	}
}

func TestBuildTranslateJobsDirectoryKeepsRelativeStructure(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "docs")
	output := filepath.Join(tmp, "translated")
	writeTestFile(t, filepath.Join(root, "a.md"))
	writeTestFile(t, filepath.Join(root, "nested", "b.txt"))
	writeTestFile(t, filepath.Join(root, "skip.bin"))

	jobs, report, err := buildTranslateJobs([]string{root}, output)
	if err != nil {
		t.Fatalf("buildTranslateJobs() error = %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(jobs))
	}
	if len(report.Ignored) != 1 {
		t.Fatalf("ignored = %d, want 1", len(report.Ignored))
	}
	if got, want := jobs[0].OutputPath, filepath.Join(output, "a.md"); got != want {
		t.Fatalf("jobs[0].OutputPath = %q, want %q", got, want)
	}
	if got, want := jobs[1].OutputPath, filepath.Join(output, "nested", "b.txt"); got != want {
		t.Fatalf("jobs[1].OutputPath = %q, want %q", got, want)
	}
	if got, want := report.Ignored[0].Path, filepath.Join(root, "skip.bin"); got != want {
		t.Fatalf("ignored[0].Path = %q, want %q", got, want)
	}
}

func TestBuildTranslateJobsMixedInputsIgnoreDuplicateAndUnsupported(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "docs")
	output := filepath.Join(tmp, "translated")
	shared := filepath.Join(root, "shared.md")
	writeTestFile(t, shared)
	writeTestFile(t, filepath.Join(root, "extra.txt"))
	unsupported := filepath.Join(tmp, "note.bin")
	writeTestFile(t, unsupported)

	jobs, report, err := buildTranslateJobs([]string{shared, root, unsupported}, output)
	if err != nil {
		t.Fatalf("buildTranslateJobs() error = %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(jobs))
	}
	if len(report.Ignored) != 1 {
		t.Fatalf("ignored = %d, want 1", len(report.Ignored))
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
