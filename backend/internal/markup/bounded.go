package markup

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func init() { backend.RegisterFixedRegexp(tagRe) }

func ValidateFragmentBounded(b *backend.BuildBounds, s string) error {
	if s == "" {
		return nil
	}
	if e := backend.ReserveXML(b, s); e != nil {
		return e
	}
	reader := io.MultiReader(strings.NewReader("<"+fragmentRoot+">"), strings.NewReader(s), strings.NewReader("</"+fragmentRoot+">"))
	dec := xml.NewDecoder(reader)
	for {
		_, e := dec.Token()
		if errors.Is(e, io.EOF) {
			return nil
		}
		if e != nil {
			return fragmentError(e)
		}
	}
}
func TargetRegressionBounded(b *backend.BuildBounds, source, target string) error {
	loc, e := backend.FindRegexpIndex(b, tagRe, source)
	if e != nil {
		return e
	}
	if loc == nil {
		return nil
	}
	if e := ValidateFragmentBounded(b, source); e != nil {
		if errors.Is(e, backend.ErrBuildCapacity) {
			return e
		}
		return nil
	}
	return ValidateFragmentBounded(b, target)
}
