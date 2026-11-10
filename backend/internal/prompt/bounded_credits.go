package prompt

import (
	"fmt"
	"reflect"
	"text/template"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func renderPairBounded(b *backend.BuildBounds, t *template.Template, d any, maxBytes int64, claim func(backend.PromptMeasure) error, user func(*textOutput)) (string, string, error) {
	if e := validateTemplateData(b, reflect.ValueOf(d), 0); e != nil {
		return "", "", e
	}
	frozen, e := b.Clone(d)
	if e != nil {
		return "", "", e
	}
	bt, e := boundedTemplate(b, t, maxBytes)
	if e != nil {
		return "", "", e
	}
	system := func(o *textOutput) { o.err = bt.Execute(o.w, frozen) }
	sc := &textCount{limit: maxBytes}
	so := &textOutput{w: sc}
	system(so)
	if so.err != nil {
		return "", "", so.err
	}
	uc := &textCount{limit: maxBytes - sc.n}
	uo := &textOutput{w: uc}
	user(uo)
	if uo.err != nil {
		return "", "", uo.err
	}
	m := backend.PromptMeasure{SystemBytes: sc.n, UserBytes: uc.n, SystemJSONBytes: sc.json + 2, UserJSONBytes: uc.json + 2}
	if claim != nil {
		if e = claim(m); e != nil {
			return "", "", e
		}
	}
	encode := func(n int64, fn func(*textOutput)) (string, error) {
		w, e := backend.NewFixedBoundedWriter(b, n)
		if e != nil {
			return "", e
		}
		o := &textOutput{w: w}
		fn(o)
		if o.err != nil {
			return "", o.err
		}
		if int64(len(w.Bytes())) != n {
			return "", fmt.Errorf("prompt changed during rendering")
		}
		if e = b.ReserveText(n, n); e != nil {
			return "", e
		}
		return string(w.Bytes()), nil
	}
	s, e := encode(sc.n, system)
	if e != nil {
		return "", "", e
	}
	u, e := encode(uc.n, user)
	if e != nil {
		return "", "", e
	}
	ms, e := backend.MeasureJSONString(s)
	if e != nil {
		return "", "", e
	}
	mu, e := backend.MeasureJSONString(u)
	if e != nil {
		return "", "", e
	}
	if ms != m.SystemJSONBytes || mu != m.UserJSONBytes {
		return "", "", fmt.Errorf("prompt escaping changed during rendering")
	}
	return s, u, nil
}
