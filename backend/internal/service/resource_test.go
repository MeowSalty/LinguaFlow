package service

import (
	"errors"
	"testing"
)

func TestNormalizeResourcePathAllowsSameBasenameInDifferentDirectories(t *testing.T) {
	cases := map[string]string{
		"ui/common.json":    "ui/common.json",
		"admin/common.json": "admin/common.json",
		`ui\common.json`:    "ui/common.json",
		" ui/common.json ":  "ui/common.json",
	}

	for input, want := range cases {
		got, err := NormalizeResourcePath(input)
		if err != nil {
			t.Fatalf("NormalizeResourcePath(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeResourcePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeResourcePathRejectsTraversalAndEmptyPath(t *testing.T) {
	cases := []string{"", ".", "..", "../common.json", "/common.json", "ui/../common.json"}
	for _, input := range cases {
		_, err := NormalizeResourcePath(input)
		if !errors.Is(err, ErrResourcePathInvalid) {
			t.Fatalf("NormalizeResourcePath(%q) error = %v, want ErrResourcePathInvalid", input, err)
		}
	}
}

func TestNormalizeResourcePathSanitizesInvalidFilenameChars(t *testing.T) {
	got, err := NormalizeResourcePath(`ui/com:mon?.json`)
	if err != nil {
		t.Fatalf("NormalizeResourcePath returned error: %v", err)
	}
	if got != "ui/com_mon_.json" {
		t.Fatalf("NormalizeResourcePath = %q, want ui/com_mon_.json", got)
	}
}
