package ruby

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	htmltoken "golang.org/x/net/html"
)

func BuildTranslationRegionsBounded(b *backend.BuildBounds, translation, format string) ([]TranslationRegion, error) {
	if !utf8.ValidString(translation) {
		return nil, errors.New("ruby candidate is not valid UTF-8")
	}
	xmlMode := strings.EqualFold(format, "epub")
	markupMode := xmlMode || strings.EqualFold(format, "html") || strings.EqualFold(format, "docx")
	if !markupMode && strings.Contains(translation, "<") {
		loc, e := backend.FindRegexpIndex(b, alignmentTagRE, translation)
		if e != nil {
			return nil, e
		}
		markupMode = loc != nil
	}
	if !markupMode {
		if translation == "" {
			return nil, nil
		}
		if e := b.ReserveNodes(2, 128); e != nil {
			return nil, e
		}
		return []TranslationRegion{{Text: translation, Mappings: []RegionMapping{{ViewEnd: len(translation), RawEnd: len(translation), Linear: true}}}}, nil
	}
	if e := backend.ReserveXML(b, translation); e != nil {
		return nil, e
	}
	if xmlMode {
		dec := xml.NewDecoder(io.MultiReader(strings.NewReader("<lf-ruby-view>"), strings.NewReader(translation), strings.NewReader("</lf-ruby-view>")))
		for {
			_, e := dec.Token()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				return nil, fmt.Errorf("ruby candidate XML: %w", e)
			}
		}
	}
	z := htmltoken.NewTokenizer(strings.NewReader(translation))
	z.SetMaxBuf(len(translation) + 1)
	var out []TranslationRegion
	var excluded []string
	pos := 0
	for {
		kind := z.Next()
		if kind == htmltoken.ErrorToken {
			if errors.Is(z.Err(), io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("ruby candidate markup: %w", z.Err())
		}
		start := pos
		pos += len(z.Raw())
		raw := translation[start:pos]
		switch kind {
		case htmltoken.StartTagToken, htmltoken.EndTagToken:
			name, _ := z.TagName()
			tag, e := lowerTagBounded(b, name)
			if e != nil {
				return nil, e
			}
			tag = localTagName(tag)
			if kind == htmltoken.StartTagToken {
				if len(excluded) > 0 || excludedRubyRegion(tag) {
					if len(excluded) >= 64 {
						return nil, &backend.BuildCapacityError{Resource: "ruby excluded depth"}
					}
					var e error
					excluded, e = backend.AppendNode(b, excluded, tag)
					if e != nil {
						return nil, e
					}
				}
			} else {
				for i := len(excluded) - 1; i >= 0; i-- {
					if excluded[i] == tag {
						excluded = excluded[:i]
						break
					}
				}
			}
		case htmltoken.TextToken:
			if len(excluded) > 0 || raw == "" {
				continue
			}
			r, e := decodeRegionBounded(b, raw, start, xmlMode)
			if e != nil {
				return nil, e
			}
			if r.Text != "" {
				out, e = backend.AppendNode(b, out, r)
				if e != nil {
					return nil, e
				}
			}
		}
	}
}
func lowerTagBounded(b *backend.BuildBounds, name []byte) (string, error) {
	n := int64(0)
	changed := false
	for rest := name; len(rest) > 0; {
		r, size := utf8.DecodeRune(rest)
		lower := unicode.ToLower(r)
		changed = changed || lower != r
		n += int64(utf8.RuneLen(lower))
		rest = rest[size:]
	}
	if !changed {
		if e := b.ReserveText(int64(len(name)), int64(len(name))); e != nil {
			return "", e
		}
		return string(name), nil
	}
	w, e := backend.NewFixedBoundedWriter(b, n)
	if e != nil {
		return "", e
	}
	var scratch [4]byte
	for len(name) > 0 {
		r, size := utf8.DecodeRune(name)
		count := utf8.EncodeRune(scratch[:], unicode.ToLower(r))
		if _, e = w.Write(scratch[:count]); e != nil {
			return "", e
		}
		name = name[size:]
	}
	if e = b.ReserveText(n, n); e != nil {
		return "", e
	}
	return string(w.Bytes()), nil
}

func decodeRegionBounded(b *backend.BuildBounds, raw string, rawStart int, xmlMode bool) (TranslationRegion, error) {
	w := backend.NewBoundedWriter(b, int64(len(raw)))
	var mappings []RegionMapping
	appendPiece := func(text string, start, end int, linear bool) error {
		if text == "" {
			return nil
		}
		viewStart := len(w.Bytes())
		if _, e := w.WriteString(text); e != nil {
			return e
		}
		if linear && len(mappings) > 0 {
			last := &mappings[len(mappings)-1]
			if last.Linear && last.RawEnd == rawStart+start {
				last.ViewEnd = len(w.Bytes())
				last.RawEnd = rawStart + end
				return nil
			}
		}
		var e error
		mappings, e = backend.AppendNode(b, mappings, RegionMapping{viewStart, len(w.Bytes()), rawStart + start, rawStart + end, linear})
		return e
	}
	for i := 0; i < len(raw); {
		if raw[i] == '\r' {
			end := i + 1
			if end < len(raw) && raw[end] == '\n' {
				end++
			}
			if e := appendPiece("\n", i, end, false); e != nil {
				return TranslationRegion{}, e
			}
			i = end
			continue
		}
		if raw[i] == '&' {
			if !xmlMode {
				decoded, consumed, e := decodeHTMLRegionEntityBounded(b, raw[i:])
				if e != nil {
					return TranslationRegion{}, e
				}
				if consumed > 0 {
					if e = appendPiece(decoded, i, i+consumed, false); e != nil {
						return TranslationRegion{}, e
					}
					i += consumed
					continue
				}
			} else if end := strings.IndexByte(raw[i:], ';'); end >= 0 && end <= 64 {
				end += i + 1
				encoded := raw[i:end]
				if e := b.ReserveText(0, 4*int64(len(encoded)+7)); e != nil {
					return TranslationRegion{}, e
				}
				if e := b.ReserveNodes(4, 1024); e != nil {
					return TranslationRegion{}, e
				}
				var value string
				if e := xml.Unmarshal([]byte("<v>"+encoded+"</v>"), &value); e != nil {
					return TranslationRegion{}, fmt.Errorf("ruby entity: %w", e)
				}
				if value != encoded {
					if e := appendPiece(value, i, end, false); e != nil {
						return TranslationRegion{}, e
					}
					i = end
					continue
				}
			}
		}
		_, size := utf8.DecodeRuneInString(raw[i:])
		if e := appendPiece(raw[i:i+size], i, i+size, true); e != nil {
			return TranslationRegion{}, e
		}
		i += size
	}
	if e := b.ReserveText(int64(len(w.Bytes())), int64(len(w.Bytes()))); e != nil {
		return TranslationRegion{}, e
	}
	return TranslationRegion{Text: string(w.Bytes()), Mappings: mappings}, nil
}
func decodeHTMLRegionEntityBounded(b *backend.BuildBounds, raw string) (string, int, error) {
	end := 1
	for end < len(raw) {
		c := raw[end]
		if c == ';' {
			end++
			break
		}
		if c != '#' && !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
			break
		}
		end++
	}
	encoded := raw[:end]
	if e := b.ReserveText(0, 2*int64(len(encoded))); e != nil {
		return "", 0, e
	}
	decoded := html.UnescapeString(encoded)
	if decoded == encoded {
		return "", 0, nil
	}
	rawEnd, decodedEnd := len(encoded), len(decoded)
	for rawEnd > 1 && decodedEnd > 1 && encoded[rawEnd-1] == decoded[decodedEnd-1] {
		rawEnd--
		decodedEnd--
	}
	if e := b.ReserveText(0, 2*int64(rawEnd)); e != nil {
		return "", 0, e
	}
	if html.UnescapeString(encoded[:rawEnd]) != decoded[:decodedEnd] {
		return decoded, len(encoded), nil
	}
	return decoded[:decodedEnd], rawEnd, nil
}
