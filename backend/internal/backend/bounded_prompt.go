package backend

import (
	"fmt"
	"io"
)

type promptTextCounter struct{ bytes, escaped, limit int64 }

func (c *promptTextCounter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.limit-c.bytes {
		return 0, &BuildCapacityError{"prompt user bytes"}
	}
	c.bytes += int64(len(p))
	c.escaped += MeasureJSONStringBytes(p) - 2
	return len(p), nil
}
func (c *promptTextCounter) WriteString(s string) (int, error) {
	if int64(len(s)) > c.limit-c.bytes {
		return 0, &BuildCapacityError{"prompt user bytes"}
	}
	c.bytes += int64(len(s))
	n, e := MeasureJSONString(s)
	if e != nil {
		return 0, e
	}
	c.escaped += n - 2
	return len(s), nil
}

// BuildUserWithCredits counts a frozen user body and its outer JSON escaping,
// obtains exact numeric credit, then allocates one exact output backing plus
// its final string. The callback is the caller's existing ledger operation.
func BuildUserWithCredits(b *BuildBounds, system string, maxBytes int64, claim func(PromptMeasure) error, write func(io.Writer) error) (string, error) {
	if int64(len(system)) > maxBytes {
		return "", &BuildCapacityError{"prompt system bytes"}
	}
	if e := b.ReserveNodes(4, 1024); e != nil {
		return "", e
	}
	c := promptTextCounter{limit: maxBytes - int64(len(system))}
	if e := write(&c); e != nil {
		return "", e
	}
	systemJSON, e := MeasureJSONString(system)
	if e != nil {
		return "", e
	}
	m := PromptMeasure{SystemBytes: int64(len(system)), UserBytes: c.bytes, SystemJSONBytes: systemJSON, UserJSONBytes: c.escaped + 2}
	if claim != nil {
		if e = claim(m); e != nil {
			return "", e
		}
	}
	w, e := NewFixedBoundedWriter(b, c.bytes)
	if e != nil {
		return "", e
	}
	if e = write(w); e != nil {
		return "", e
	}
	if int64(len(w.Bytes())) != c.bytes {
		return "", fmt.Errorf("user prompt changed during rendering")
	}
	if e = b.ReserveText(c.bytes, c.bytes); e != nil {
		return "", e
	}
	text := string(w.Bytes())
	n, e := MeasureJSONString(text)
	if e != nil {
		return "", e
	}
	if n != m.UserJSONBytes {
		return "", fmt.Errorf("user prompt escaping changed during rendering")
	}
	return text, nil
}
