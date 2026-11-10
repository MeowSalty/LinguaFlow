package backend

import (
	"math"
	"reflect"
	"unicode/utf8"
)

type PromptMeasure struct{ SystemBytes, UserBytes, SystemJSONBytes, UserJSONBytes int64 }

func (m PromptMeasure) Validate() error {
	for _, p := range [2][2]int64{{m.SystemBytes, m.SystemJSONBytes}, {m.UserBytes, m.UserJSONBytes}} {
		if p[0] < 0 || p[0] > math.MaxInt64-2 || p[1] < p[0]+2 {
			return &BuildCapacityError{"prompt measure"}
		}
		if p[0] <= (math.MaxInt64-2)/6 && p[1] > 6*p[0]+2 {
			return &BuildCapacityError{"prompt escaping measure"}
		}
	}
	return nil
}

type measuredJSONString struct{ bytes int64 }

var measuredJSONStringType = reflect.TypeFor[measuredJSONString]()

func MeasureJSONString(s string) (int64, error) {
	c := jsonCounter{limit: math.MaxInt64}
	if e := writeJSONString(&c, s); e != nil {
		return 0, e
	}
	return c.bytes, nil
}
func MeasureJSONStringBytes(p []byte) int64 {
	n := int64(2)
	for i := 0; i < len(p); {
		c := p[i]
		if c == '"' || c == '\\' || shortJSONEscape(c) != 0 {
			n += 2
			i++
			continue
		}
		if c < 32 || c == '<' || c == '>' || c == '&' {
			n += 6
			i++
			continue
		}
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size == 1 {
			n += 6
		} else if r == 0x2028 || r == 0x2029 {
			n += 6
		} else {
			n += int64(size)
		}
		i += size
	}
	return n
}
func PromptValues(req Request, m *PromptMeasure, suffix string) (system, user any, err error) {
	if m == nil {
		if suffix == "" {
			return req.System, req.User, nil
		}
		return WireString{req.System, suffix}, req.User, nil
	}
	if e := m.Validate(); e != nil {
		return nil, nil, e
	}
	n, e := MeasureJSONString(suffix)
	if e != nil {
		return nil, nil, e
	}
	if n-2 > math.MaxInt64-m.SystemJSONBytes {
		return nil, nil, &BuildCapacityError{"prompt measure arithmetic"}
	}
	return measuredJSONString{m.SystemJSONBytes + n - 2}, measuredJSONString{m.UserJSONBytes}, nil
}

type PromptWireSizer interface {
	MeasureRequestPrompt(Request, PromptMeasure) (int64, error)
}
