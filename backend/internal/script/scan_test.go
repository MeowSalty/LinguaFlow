package script

import (
	"strings"
	"testing"
)

func TestScan(t *testing.T) {
	zhHans := Profile{Language: "zh", Expected: Hans, Siblings: []Script{Hant}}
	srLatn := Profile{Language: "sr", Expected: Latn, Siblings: []Script{Cyrl}}
	cases := []struct {
		name       string
		profile    Profile
		text       string
		wantScript Script
		wantCount  int
		wantSample string
		wantOK     bool
	}{
		{
			// 首个繁体 run 是「這」，后续「簡體」「測試」「體」为独立 run,共 6 个专属字
			"纯繁体报 Hant 证据", zhHans, "這是簡體字測試的繁體版本",
			Hant, 6, "這", true,
		},
		{"纯简体无证据", zhHans, "这是测试", Script(""), 0, "", false},
		{"全共用字无证据", zhHans, "人山川的", Script(""), 0, "", false},
		{
			// 仅「裡」一个繁体专属字，其余为简体/共用
			"简体混单个繁体字", zhHans, "这是测试,裡面有一个繁体字",
			Hant, 1, "裡", true,
		},
		{
			"sr-Latn 混西里尔", srLatn, "Dobro došli Жељко",
			Cyrl, 5, "Жељко", true,
		},
		{"ASCII 数字标点不计", zhHans, "Hello World 123 !? <html>", Script(""), 0, "", false},
		{
			// 两段独立繁体 run,Sample 取首个
			"Sample 取首个 run", zhHans, "裡一裡",
			Hant, 2, "裡", true,
		},
	}
	for _, tc := range cases {
		got, ok := tc.profile.Scan(tc.text)
		if ok != tc.wantOK {
			t.Errorf("%s: Scan(%q) ok = %v, want %v", tc.name, tc.text, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if got.Script != tc.wantScript || got.Count != tc.wantCount {
			t.Errorf("%s: Scan(%q) = (Script %q, Count %d), want (%q, %d)",
				tc.name, tc.text, got.Script, got.Count, tc.wantScript, tc.wantCount)
		}
		if got.Sample != tc.wantSample {
			t.Errorf("%s: Scan(%q) Sample = %q, want %q", tc.name, tc.text, got.Sample, tc.wantSample)
		}
		// Sample 必须是原文的连续片段，保证上层 span 定位可用
		if !strings.Contains(tc.text, got.Sample) {
			t.Errorf("%s: Sample %q 不是原文 %q 的连续片段", tc.name, got.Sample, tc.text)
		}
	}
}

// TestScanTieBreakBySiblingOrder 固化并列决胜规则：计数相同时取 Siblings 顺序靠前者。
func TestScanTieBreakBySiblingOrder(t *testing.T) {
	uz := Profile{Language: "uz", Expected: Latn, Siblings: []Script{Cyrl, Arab}}
	got, ok := uz.Scan("وا ЖЖ") // Arab 2 字 vs Cyrl 2 字,并列
	if !ok {
		t.Fatal("Scan 并列用例应产生证据")
	}
	if got.Script != Cyrl {
		t.Errorf("并列时应取 Siblings 顺序靠前的 %q,得到 %q", Cyrl, got.Script)
	}
	if got.Count != 2 {
		t.Errorf("Count = %d, want 2", got.Count)
	}
	// 计数不等时取大者
	got, ok = uz.Scan("Привет مرحبا") // Cyrl 6 字 > Arab 5 字
	if !ok || got.Script != Cyrl || got.Count != 6 {
		t.Errorf("计数大者应胜出,得到 (%q, %d, %v)", got.Script, got.Count, ok)
	}
}

// TestScanSampleTruncated 固化 Sample 的 12 rune 截断：截断后仍是原文前缀片段。
func TestScanSampleTruncated(t *testing.T) {
	p := Profile{Language: "sr", Expected: Cyrl, Siblings: []Script{Latn}}
	got, ok := p.Scan("ABCDEFGHIJKLMNO") // 15 个拉丁字母
	if !ok {
		t.Fatal("Scan 应产生证据")
	}
	if got.Count != 15 {
		t.Errorf("Count = %d, want 15", got.Count)
	}
	if runeCount := len([]rune(got.Sample)); runeCount != maxSampleRunes {
		t.Errorf("Sample 应截断至 %d rune,实际 %d", maxSampleRunes, runeCount)
	}
	if got.Sample != "ABCDEFGHIJKL" {
		t.Errorf("Sample = %q, want 前 12 个字母", got.Sample)
	}
	if !strings.Contains("ABCDEFGHIJKLMNO", got.Sample) {
		t.Errorf("截断后的 Sample %q 应仍可在原文中找到", got.Sample)
	}
}

// TestScanEmptyProfile 零值 Profile 无 sibling，不应产生证据。
func TestScanEmptyProfile(t *testing.T) {
	if _, ok := (Profile{}).Scan("任何文本 見ЖA"); ok {
		t.Error("空 Siblings 的 Profile 不应产生证据")
	}
}

// TestScanAmphibiousChars 固化两栖字豁免的实际效果：sharedOverride 中的字
// 在简繁两侧文本里都是规范用法，任何一侧都不产生证据；且豁免不削弱对
// 整段反向文字的检出（同句中的其他专属字照常计证据）。
func TestScanAmphibiousChars(t *testing.T) {
	zhHans := Profile{Language: "zh", Expected: Hans, Siblings: []Script{Hant}}
	zhHant := Profile{Language: "zh", Expected: Hant, Siblings: []Script{Hans}}
	cases := []struct {
		name    string
		profile Profile
		text    string
		wantOK  bool
	}{
		{"繁体文本含两栖字不报", zhHant, "台灣批准游泳長征山峰文采", false},
		{"简体文本含两栖字不报", zhHans, "台湾批准游泳长征山峰文采", false},
		{"繁体段中的两栖字不掩护其他繁体字", zhHans, "這是台灣", true},
	}
	for _, tc := range cases {
		if _, ok := tc.profile.Scan(tc.text); ok != tc.wantOK {
			t.Errorf("%s: Scan(%q) ok = %v, want %v", tc.name, tc.text, ok, tc.wantOK)
		}
	}
	// 豁免只去掉两栖字本身：這/灣 两个繁体专属字仍应完整计数
	got, ok := zhHans.Scan("這是台灣")
	if !ok || got.Script != Hant || got.Count != 2 || got.Sample != "這" {
		t.Errorf("Scan(這是台灣) = (%q, %d, %q, %v), want (Hant, 2, 這, true)", got.Script, got.Count, got.Sample, ok)
	}
}
