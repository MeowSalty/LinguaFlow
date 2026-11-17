package backend

import (
	"bytes"
	"errors"
	"reflect"
)

// WireJSONValue traverses a validated wire envelope without an offset arena or
// typed choices/parts slices. Raw tokens borrow the caller's response buffer.
// A returned child cannot outlive that buffer. Field lookup keeps the last
// duplicate field, matching typed JSON decoding.
type WireJSONValue struct {
	b     *BuildBounds
	data  []byte
	depth int
}

func BorrowWireJSON(b *BuildBounds, data []byte) (WireJSONValue, error) {
	if err := b.CheckContext(); err != nil {
		return WireJSONValue{}, err
	}
	if int64(len(data)) > b.inputBytes+b.outputBytes {
		return WireJSONValue{}, &BuildCapacityError{"JSON input bytes"}
	}
	if _, err := scanJSON(data, nil, int64(len(data))); err != nil {
		return WireJSONValue{}, err
	}
	return WireJSONValue{b: b, data: bytes.TrimSpace(data)}, nil
}
func (v WireJSONValue) Kind() byte {
	if len(v.data) == 0 {
		return 0
	}
	return v.data[0]
}
func (v WireJSONValue) Raw() []byte  { return v.data }
func (v WireJSONValue) IsNull() bool { return v.Kind() == 0 || v.Kind() == 'n' }
func (v WireJSONValue) StringEquals(want string) bool {
	return v.Kind() == '"' && jsonStringEquals(v.data, want)
}

func (v WireJSONValue) EachArray(consume func(WireJSONValue) error) error {
	if v.IsNull() {
		return nil
	}
	if v.Kind() != '[' {
		return errors.New("expected JSON array")
	}
	return v.each(false, func(_, value WireJSONValue) error { return consume(value) })
}
func (v WireJSONValue) EachField(consume func(WireJSONValue, WireJSONValue) error) error {
	if v.IsNull() {
		return nil
	}
	if v.Kind() != '{' {
		return errors.New("expected JSON object")
	}
	return v.each(true, consume)
}
func (v WireJSONValue) Field(name string) (WireJSONValue, error) {
	var found WireJSONValue
	err := v.EachField(func(key, value WireJSONValue) error {
		if key.StringEquals(name) {
			found = value
		}
		return nil
	})
	return found, err
}
func (v WireJSONValue) Path(names ...string) (WireJSONValue, error) {
	var err error
	for _, name := range names {
		v, err = v.Field(name)
		if err != nil {
			return WireJSONValue{}, err
		}
	}
	return v, nil
}
func (v WireJSONValue) DecodeScalar(out any) error { return v.decodeScalar(out) }
func (v WireJSONValue) First() (WireJSONValue, error) {
	var first WireJSONValue
	err := v.EachArray(func(item WireJSONValue) error {
		if first.b == nil {
			first = item
		}
		return nil
	})
	return first, err
}
func (v WireJSONValue) StringCode(codes ...string) string {
	for _, code := range codes {
		if v.StringEquals(code) {
			return code
		}
	}
	return ""
}

// WireStringSlot owns a selected scalar across events and drops the previous
// backing before replacing it. Close follows the final use of Value.
type WireStringSlot struct {
	Value string
	owner *OwnedAllocation
}

func (s *WireStringSlot) Set(value WireJSONValue) error {
	s.Close()
	var err error
	s.Value, s.owner, err = value.OwnedString()
	return err
}
func (s *WireStringSlot) Close() { s.Value = ""; s.owner.Release(); s.owner = nil }
func (v WireJSONValue) each(object bool, consume func(WireJSONValue, WireJSONValue) error) error {
	s := jsonScan{data: v.data, pos: 1, limit: int64(len(v.data)), maxDepth: MaxBuildDepth - v.depth - 1}
	close := byte(']')
	if object {
		close = '}'
	}
	for {
		if err := v.b.CheckContext(); err != nil {
			return err
		}
		s.space()
		if s.data[s.pos] == close {
			return nil
		}
		var key WireJSONValue
		if object {
			start := s.pos
			if _, err := s.value(); err != nil {
				return err
			}
			key = WireJSONValue{v.b, s.data[start:s.pos], v.depth + 1}
			s.space()
			s.pos++
			s.space() // validated colon
		}
		start := s.pos
		if _, err := s.value(); err != nil {
			return err
		}
		value := WireJSONValue{v.b, s.data[start:s.pos], v.depth + 1}
		if err := consume(key, value); err != nil {
			return err
		}
		s.space()
		if s.data[s.pos] == close {
			return nil
		}
		s.pos++ // validated comma
	}
}

// OwnedString materializes only a selected string token. Release is valid once
// it has been copied into its destination or its final borrower has ended.
func (v WireJSONValue) OwnedString() (string, *OwnedAllocation, error) {
	if v.IsNull() {
		return "", nil, nil
	}
	if v.Kind() != '"' {
		return "", nil, errors.New("expected JSON string")
	}
	return v.b.ownedJSONString(v.data)
}
func (v WireJSONValue) AppendString(w *ResponseText) error {
	s, owner, err := v.OwnedString()
	if err != nil {
		return err
	}
	defer owner.Release()
	return w.Append(s)
}
func (v WireJSONValue) Int64() (int64, error) {
	var value int64
	if err := v.decodeScalar(&value); err != nil {
		return 0, err
	}
	return value, nil
}
func (v WireJSONValue) Int() (int, error) {
	var value int
	if err := v.decodeScalar(&value); err != nil {
		return 0, err
	}
	return value, nil
}
func (v WireJSONValue) Bool() (bool, error) {
	var value bool
	if err := v.decodeScalar(&value); err != nil {
		return false, err
	}
	return value, nil
}
func (v WireJSONValue) decodeScalar(dst any) error {
	if v.IsNull() {
		return nil
	}
	if v.Kind() == '{' || v.Kind() == '[' {
		return errors.New("expected JSON scalar")
	}
	nodes := [1]jsonNode{{start: 0, end: len(v.data), child: -1, next: -1, kind: v.Kind()}}
	return v.b.decodeValue(v.data, nodes[:], 0, reflect.ValueOf(dst).Elem())
}
