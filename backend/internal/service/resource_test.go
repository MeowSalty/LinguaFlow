package service

import (
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestDiffSegmentsRefreshesMetaOnUnchanged(t *testing.T) {
	oldMeta := `{"md_block":"paragraph","md_byte_range":[0,5]}`
	old := []*ent.Segment{
		{
			ID:         1,
			SourceText: "Hello",
			Meta:       &oldMeta,
		},
	}
	newSegs := []parsedResourceSegment{
		{
			Index:      0,
			SourceText: "Hello",
			Meta: map[string]any{
				"md_block":      "paragraph",
				"md_byte_range": []int{10, 15}, // 上方编辑后偏移已漂移
			},
		},
	}

	changes := diffSegments(old, newSegs)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(changes))
	}
	c := changes[0]
	if c.ChangeType != SegmentChangeUnchanged {
		t.Fatalf("ChangeType = %s, want unchanged", c.ChangeType)
	}
	if c.NewMeta == nil {
		t.Fatal("NewMeta is nil, want refreshed meta")
	}
	pos, ok := c.NewMeta["md_byte_range"].([]int)
	if !ok || len(pos) != 2 || pos[0] != 10 || pos[1] != 15 {
		t.Fatalf("NewMeta md_byte_range = %#v, want [10 15]", c.NewMeta["md_byte_range"])
	}
}

func TestDiffSegmentsRefreshesMetaOnUpdated(t *testing.T) {
	// 匹配键为 TrimSpace(source)，匹配后再用原始 source 比较：
	// 仅首尾空白差异（"Hello" vs "Hello\n"）时走 Updated。
	oldMeta := `{"md_block":"paragraph","md_byte_range":[0,5]}`
	old := []*ent.Segment{
		{
			ID:         1,
			SourceText: "Hello",
			Meta:       &oldMeta,
		},
	}
	newSegs := []parsedResourceSegment{
		{
			Index:      0,
			SourceText: "Hello\n", // trim 后与 "Hello" 相同，但原始字符不同
			Meta: map[string]any{
				"md_block":      "paragraph",
				"md_byte_range": []int{10, 16}, // 偏移漂移后的新区间
			},
		},
	}
	changes := diffSegments(old, newSegs)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(changes), changes)
	}
	c := changes[0]
	if c.ChangeType != SegmentChangeUpdated {
		t.Fatalf("ChangeType = %s, want updated", c.ChangeType)
	}
	if c.NewMeta == nil {
		t.Fatal("NewMeta is nil, want refreshed meta")
	}
	pos, ok := c.NewMeta["md_byte_range"].([]int)
	if !ok || len(pos) != 2 || pos[0] != 10 || pos[1] != 16 {
		t.Fatalf("NewMeta md_byte_range = %#v, want [10 16]", c.NewMeta["md_byte_range"])
	}
}

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
