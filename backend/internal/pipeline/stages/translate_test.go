package stages

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestParseBatchResponse_OK(t *testing.T) {
	resp := `{"translations":{"1":"hello","2":"world"}}`
	got, err := parseBatchResponse(resp, []string{"1", "2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "hello" || got["2"] != "world" {
		t.Fatalf("unexpected parts: %#v", got)
	}
}

func TestParseBatchResponse_PreservesInternalNewlines(t *testing.T) {
	resp := `{"translations":{"1":"line1\nline2"}}`
	got, err := parseBatchResponse(resp, []string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "line1\nline2" {
		t.Fatalf("internal newline lost: %q", got["1"])
	}
}

func TestParseBatchResponse_MissingID(t *testing.T) {
	resp := `{"translations":{"1":"a"}}`
	if _, err := parseBatchResponse(resp, []string{"1", "2"}); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestParseBatchResponse_ExtraID(t *testing.T) {
	resp := `{"translations":{"1":"a","2":"b","3":"c"}}`
	_, err := parseBatchResponse(resp, []string{"1", "2"})
	if err == nil {
		t.Fatal("expected error for extra translation")
	}
}

func TestParseBatchResponse_IgnoresCodeFenceAndPreamble(t *testing.T) {
	// 模型偶尔在 JSON 前后多说话或加 ``` 围栏；只要能找到 {…} 就接受。
	resp := "Sure! Here you go:\n```json\n{\"translations\":{\"1\":\"a\",\"2\":\"b\"}}\n```\nDone."
	got, err := parseBatchResponse(resp, []string{"1", "2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "a" || got["2"] != "b" {
		t.Fatalf("unexpected parts: %#v", got)
	}
}

func TestParseBatchResponse_HandlesEscapedBraceInValue(t *testing.T) {
	// 译文里出现 `}` 或转义引号时，jsonObjectSlice 必须能正确配对。
	resp := `{"translations":{"1":"value with } and \"quote\" inside"}}`
	got, err := parseBatchResponse(resp, []string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `value with } and "quote" inside`
	if got["1"] != want {
		t.Fatalf("got %q want %q", got["1"], want)
	}
}

func TestParseBatchResponse_NotJSON(t *testing.T) {
	if _, err := parseBatchResponse("totally not json", []string{"1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestTranslationsSchema_Shape(t *testing.T) {
	schema := translationsSchema([]string{"1", "2", "3"})
	if schema["additionalProperties"] != false {
		t.Errorf("outer additionalProperties should be false")
	}
	outerRequired, _ := schema["required"].([]string)
	if !reflect.DeepEqual(outerRequired, []string{"translations"}) {
		t.Errorf("outer required mismatch: %#v", outerRequired)
	}
	props := schema["properties"].(map[string]any)
	tr := props["translations"].(map[string]any)
	if tr["type"] != "object" || tr["additionalProperties"] != false {
		t.Errorf("translations object shape wrong: %#v", tr)
	}
	req, _ := tr["required"].([]string)
	if !reflect.DeepEqual(req, []string{"1", "2", "3"}) {
		t.Errorf("translations.required mismatch: %#v", req)
	}
	innerProps := tr["properties"].(map[string]any)
	for _, id := range []string{"1", "2", "3"} {
		p, ok := innerProps[id].(map[string]any)
		if !ok {
			t.Fatalf("missing property %q in schema: %#v", id, innerProps)
		}
		if p["type"] != "string" {
			t.Errorf("property %q type should be string, got %v", id, p["type"])
		}
	}
}

func TestJSONObjectSlice_FindsNested(t *testing.T) {
	in := `noise {"a":{"b":1}} trailing`
	got := jsonObjectSlice(in)
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Fatalf("not bracketed: %q", got)
	}
	if got != `{"a":{"b":1}}` {
		t.Fatalf("unexpected slice: %q", got)
	}
}

func TestShrinkNext(t *testing.T) {
	cases := []struct {
		name   string
		cur    int
		shrink float64
		want   int
	}{
		// 禁用：shrink 非法时一律返回 0
		{"shrink_zero", 40, 0, 0},
		{"shrink_negative", 40, -0.5, 0},
		{"shrink_one", 40, 1, 0},
		{"shrink_gt_one", 40, 1.5, 0},
		{"shrink_nan", 40, math.NaN(), 0},
		{"shrink_inf", 40, math.Inf(1), 0},

		// 正常缩小：floor(cur*shrink)
		{"half_40", 40, 0.5, 20},
		{"half_31", 31, 0.5, 15},
		{"third_30", 30, 1.0 / 3.0, 10},
		{"quarter_40", 40, 0.25, 10},

		// 收敛到 1 的边界：next<1 视作 0 走 single
		{"cur_2_half", 2, 0.5, 0},
		{"cur_3_half", 3, 0.5, 0}, // floor(1.5)=1 → 视为 0
		{"cur_4_half", 4, 0.5, 2},

		// 接近 1 的 shrink：防不收敛，强制 cur-1
		{"near_one_5", 5, 0.99, 4},
		{"near_one_10", 10, 0.95, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shrinkNext(tc.cur, tc.shrink)
			if got != tc.want {
				t.Errorf("shrinkNext(%d, %v) = %d, want %d", tc.cur, tc.shrink, got, tc.want)
			}
		})
	}
}
