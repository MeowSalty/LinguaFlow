package script

import (
	"strings"
	"testing"
)

func TestRuneScript(t *testing.T) {
	cases := []struct {
		r      rune
		want   Script
		wantOK bool
	}{
		// Han:两张专属表命中
		{'見', Hant, true},
		{'語', Hant, true},
		{'這', Hant, true},
		{'见', Hans, true},
		{'语', Hans, true},
		{'这', Hans, true},
		// Han:简繁共用，不算证据
		{'的', "", false},
		{'人', "", false},
		{'著', "", false}, // OpenCC 字级不转，推导即共用
		{'乾', "", false}, // sharedOverride 剔除
		{'准', "", false}, // sharedOverride 剔除(批准/准許)
		{'台', "", false}, // sharedOverride 剔除(台灣)
		{'游', "", false}, // sharedOverride 剔除(游泳)
		// 非 Han 文字系统
		{'A', Latn, true},
		{'ž', Latn, true},
		{'Ж', Cyrl, true},
		{'љ', Cyrl, true},
		{'ا', Arab, true},
		{'ᠠ', Mong, true}, // U+1820 蒙文 A
		{'ਗ', Guru, true}, // U+0A17 古木基文 GA
		// 中性：数字、标点、组合符号、管辖外文字
		{'1', "", false},
		{'。', "", false},
		{'!', "", false},
		{0x0301, "", false}, // 组合尖音(Inherited)
		{'Ω', "", false},    // 希腊字母不在管辖范围
		{'ひ', "", false},    // 平假名不在管辖范围
	}
	for _, tc := range cases {
		got, ok := runeScript(tc.r)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("runeScript(%q U+%04X) = (%q, %v), want (%q, %v)", tc.r, tc.r, got, ok, tc.want, tc.wantOK)
		}
	}
}

// TestTablesSortedAndDisjoint 固化生成表的基本性质：非空、按 rune 严格升序、互不相交、数量级合理。
func TestTablesSortedAndDisjoint(t *testing.T) {
	hans := []rune(hansSpecificChars)
	hant := []rune(hantSpecificChars)
	if len(hans) == 0 || len(hant) == 0 {
		t.Fatalf("生成表不应为空：hans=%d hant=%d", len(hans), len(hant))
	}
	// 数量级：简繁各约 1000–3500+ 字，过小/过大说明推导逻辑异常
	for name, rs := range map[string][]rune{"hansSpecificChars": hans, "hantSpecificChars": hant} {
		if len(rs) < 500 || len(rs) > 8000 {
			t.Errorf("%s 字符数 %d 超出合理数量级 [500, 8000]", name, len(rs))
		}
		for i := 1; i < len(rs); i++ {
			if rs[i] <= rs[i-1] {
				t.Errorf("%s 在下标 %d 处非严格升序：U+%04X <= U+%04X", name, i, rs[i], rs[i-1])
			}
		}
	}
	hantSet := make(map[rune]struct{}, len(hant))
	for _, r := range hant {
		hantSet[r] = struct{}{}
	}
	for _, r := range hans {
		if _, dup := hantSet[r]; dup {
			t.Errorf("U+%04X 同时出现在两张专属表中", r)
		}
	}
}

// TestSharedOverrideConsistency 固化 override 机制：两栖字必须确实来自推导表（防 stale），
// 且不得残留在剔除后的两张表内（防失效）。
func TestSharedOverrideConsistency(t *testing.T) {
	hans, hant := hanTables()
	for _, r := range sharedOverride {
		if _, ok := hans[r]; ok {
			t.Errorf("sharedOverride 中的 U+%04X 仍残留在简体专属表，剔除失效", r)
		}
		if _, ok := hant[r]; ok {
			t.Errorf("sharedOverride 中的 U+%04X 仍残留在繁体专属表，剔除失效", r)
		}
		if !strings.ContainsRune(hansSpecificChars, r) && !strings.ContainsRune(hantSpecificChars, r) {
			t.Errorf("sharedOverride 中的 U+%04X 已不在任何推导表中，条目过期应清理", r)
		}
	}
}

// TestTablesTypicalEntries 生成器正确性抽查：表内必含典型简繁对应字，表外必含共用字。
func TestTablesTypicalEntries(t *testing.T) {
	for _, r := range []rune("見語這裡體時") { // 繁体专属典型字
		if !strings.ContainsRune(hantSpecificChars, r) {
			t.Errorf("U+%04X 应为繁体专属，但不在 hantSpecificChars 中", r)
		}
	}
	for _, r := range []rune("见语这里体时") { // 简体专属典型字
		if !strings.ContainsRune(hansSpecificChars, r) {
			t.Errorf("U+%04X 应为简体专属，但不在 hansSpecificChars 中", r)
		}
	}
	for _, r := range []rune("的人山川好") { // 简繁共用字
		if strings.ContainsRune(hansSpecificChars, r) || strings.ContainsRune(hantSpecificChars, r) {
			t.Errorf("U+%04X 应为简繁共用，但出现在专属表中", r)
		}
	}
}
