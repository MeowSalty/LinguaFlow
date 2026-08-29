package script

import (
	"reflect"
	"testing"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name   string
		lang   string
		want   Profile
		wantOK bool
	}{
		// 显式 script 子标签
		{"zh-Hans", "zh-Hans", Profile{Language: "zh", Expected: Hans, Siblings: []Script{Hant}}, true},
		{"zh-Hant", "zh-Hant", Profile{Language: "zh", Expected: Hant, Siblings: []Script{Hans}}, true},
		{"sr-Latn", "sr-Latn", Profile{Language: "sr", Expected: Latn, Siblings: []Script{Cyrl}}, true},
		// region 参与 script 推断
		{"zh-TW", "zh-TW", Profile{Language: "zh", Expected: Hant, Siblings: []Script{Hans}}, true},
		{"zh-CN", "zh-CN", Profile{Language: "zh", Expected: Hans, Siblings: []Script{Hant}}, true},
		// likely-subtags 兜底
		{"zh", "zh", Profile{Language: "zh", Expected: Hans, Siblings: []Script{Hant}}, true},
		{"sr", "sr", Profile{Language: "sr", Expected: Cyrl, Siblings: []Script{Latn}}, true},
		{"uz", "uz", Profile{Language: "uz", Expected: Latn, Siblings: []Script{Cyrl, Arab}}, true},
		{"kk", "kk", Profile{Language: "kk", Expected: Cyrl, Siblings: []Script{Latn, Arab}}, true},
		{"az", "az", Profile{Language: "az", Expected: Latn, Siblings: []Script{Cyrl, Arab}}, true},
		{"mn", "mn", Profile{Language: "mn", Expected: Cyrl, Siblings: []Script{Mong}}, true},
		{"pa", "pa", Profile{Language: "pa", Expected: Guru, Siblings: []Script{Arab}}, true},
		{"ms", "ms", Profile{Language: "ms", Expected: Latn, Siblings: []Script{Arab}}, true},
		{"ug", "ug", Profile{Language: "ug", Expected: Arab, Siblings: []Script{Cyrl, Latn}}, true},
		{"ku", "ku", Profile{Language: "ku", Expected: Latn, Siblings: []Script{Arab, Cyrl}}, true},
		{"tg", "tg", Profile{Language: "tg", Expected: Cyrl, Siblings: []Script{Latn}}, true},
		// cmn 为汉语的 ISO 639-3 码，与 zh 同义
		{"cmn", "cmn", Profile{Language: "cmn", Expected: Hans, Siblings: []Script{Hant}}, true},
		{"cmn-Hant", "cmn-Hant", Profile{Language: "cmn", Expected: Hant, Siblings: []Script{Hans}}, true},
		// 大小写、空白、下划线容错
		{"大写", "ZH-HANS", Profile{Language: "zh", Expected: Hans, Siblings: []Script{Hant}}, true},
		{"下划线", "zh_hans", Profile{Language: "zh", Expected: Hans, Siblings: []Script{Hant}}, true},
		{"首尾空白", " zh-CN ", Profile{Language: "zh", Expected: Hans, Siblings: []Script{Hant}}, true},
		// 期望 script 不在该语言的注册表集合内
		{"pa-Arab", "pa-Arab", Profile{Language: "pa", Expected: Arab, Siblings: []Script{Guru}}, true},
		// false 用例：单文字系统语言不在注册表
		{"en", "en", Profile{}, false},
		{"fr", "fr", Profile{}, false},
		{"ja", "ja", Profile{}, false},
		{"ko", "ko", Profile{}, false},
		{"ru", "ru", Profile{}, false},
		{"th", "th", Profile{}, false},
		// false 用例：特殊与垃圾输入
		{"auto", "auto", Profile{}, false},
		{"AUTO", "AUTO", Profile{}, false},
		{"空串", "", Profile{}, false},
		{"纯空白", "  ", Profile{}, false},
		{"垃圾串", "!!!", Profile{}, false},
	}
	for _, tc := range cases {
		got, ok := Resolve(tc.lang)
		if ok != tc.wantOK {
			t.Errorf("Resolve(%q) ok = %v, want %v", tc.lang, ok, tc.wantOK)
			continue
		}
		if ok && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Resolve(%q) = %+v, want %+v", tc.lang, got, tc.want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		s    Script
		want string
	}{
		{Hans, "简体汉字"},
		{Hant, "繁体汉字"},
		{Latn, "拉丁字母"},
		{Cyrl, "西里尔字母"},
		{Arab, "阿拉伯字母"},
		{Mong, "传统蒙文"},
		{Guru, "古木基文（旁遮普语）"},
		{"Zzzz", "Zzzz"}, // 未知码原样返回
		{"", ""},
	}
	for _, tc := range cases {
		if got := tc.s.DisplayName(); got != tc.want {
			t.Errorf("Script(%q).DisplayName() = %q, want %q", tc.s, got, tc.want)
		}
	}
}
