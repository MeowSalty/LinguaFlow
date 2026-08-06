package backend

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestIsRetryable_EmptyResponseError(t *testing.T) {
	// 空响应类错误不应触发退避重试——这是 linguaflow.log:148+ 持续重试刷屏的根因。
	errs := []error{
		&EmptyResponseError{BackendName: "HUB", Model: "gpt-4o", FinishReason: "content_filter", PromptTokens: 1234},
		&EmptyResponseError{BackendName: "HUB", Model: "gpt-4o"},
	}
	for i, err := range errs {
		if IsRetryable(err) {
			t.Fatalf("case %d: IsRetryable(EmptyResponseError) = true, want false", i)
		}
	}

	// 非 EmptyResponseError 的裸错误默认可重试（网络错误等），保持既有语义不被破坏。
	plain := errors.New("openai: empty choices")
	if !IsRetryable(plain) {
		t.Fatal("plain error should be retryable by default")
	}
}

func TestIsRetryable_ContextCancellation(t *testing.T) {
	if IsRetryable(context.Canceled) {
		t.Fatal("context.Canceled should not be retryable")
	}
	if IsRetryable(context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded should not be retryable")
	}
}

func TestIsRetryable_HTTPStatusError(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{500, true},
		{503, true},
		{429, true},
		{401, false},
		{403, false},
		{400, false},
		{200, false},
	}
	for _, c := range cases {
		err := &StatusError{StatusCode: c.code, Err: errors.New("upstream")}
		if got := IsRetryable(err); got != c.want {
			t.Fatalf("IsRetryable(status=%d) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestEmptyResponseError_Message(t *testing.T) {
	t.Run("with_finish_reason_and_usage", func(t *testing.T) {
		e := &EmptyResponseError{
			BackendName:  "HUB | V4F GA",
			Model:        "gpt-4o",
			FinishReason: "content_filter",
			PromptTokens: 1234,
		}
		msg := e.Error()
		for _, want := range []string{"HUB | V4F GA", "content_filter", "gpt-4o", "prompt_tokens=1234"} {
			if !contains(msg, want) {
				t.Fatalf("error message %q missing %q", msg, want)
			}
		}
	})
	t.Run("without_usage", func(t *testing.T) {
		e := &EmptyResponseError{BackendName: "B", Model: "m", FinishReason: ""}
		msg := e.Error()
		if contains(msg, "prompt_tokens=") {
			t.Fatalf("error message should omit prompt_tokens when zero, got %q", msg)
		}
		if !contains(msg, "no usable content") {
			t.Fatalf("error message should describe empty response, got %q", msg)
		}
	})
	t.Run("nil_safe", func(t *testing.T) {
		var e *EmptyResponseError
		if e.Error() != "backend: empty response" {
			t.Fatalf("nil receiver Error() = %q", e.Error())
		}
	})
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
