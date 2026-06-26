package cli

import (
	"path/filepath"
	"testing"
)

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
