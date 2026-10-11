package ruby

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	htmltoken "golang.org/x/net/html"
)

// RegionMapping maps a half-open interval of decoded text to the untouched
// candidate. Nonlinear intervals (entities and normalized newlines) are atomic.
type RegionMapping struct {
	ViewStart int  `json:"view_start"`
	ViewEnd   int  `json:"view_end"`
	RawStart  int  `json:"raw_start"`
	RawEnd    int  `json:"raw_end"`
	Linear    bool `json:"linear"`
}

type TranslationRegion struct {
	Text     string          `json:"text"`
	Mappings []RegionMapping `json:"mappings"`
}

var alignmentTagRE = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:-]*(?:\s+[^<>]*)?/?>`)

// BuildTranslationRegions freezes the text the model can locate. Plain text is
// left byte-for-byte intact. Markup text excludes all tag syntax, comments,
// existing ruby and non-body script/style contents; regions never join nodes.
// EPUB uses XML entity/newline rules, matching its strict output renderer.
func BuildTranslationRegions(translation, format string) ([]TranslationRegion, error) {
	if !utf8.ValidString(translation) {
		return nil, errors.New("ruby candidate is not valid UTF-8")
	}
	xmlMode := strings.EqualFold(format, "epub")
	markupMode := xmlMode || strings.EqualFold(format, "html") || strings.EqualFold(format, "docx") || alignmentTagRE.MatchString(translation)
	if !markupMode {
		if translation == "" {
			return nil, nil
		}
		return []TranslationRegion{{Text: translation, Mappings: []RegionMapping{{ViewEnd: len(translation), RawEnd: len(translation), Linear: true}}}}, nil
	}
	if xmlMode {
		dec := xml.NewDecoder(strings.NewReader("<lf-ruby-view>" + translation + "</lf-ruby-view>"))
		for {
			_, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("ruby candidate XML: %w", err)
			}
		}
	}
	z := htmltoken.NewTokenizer(strings.NewReader(translation))
	var regions []TranslationRegion
	var excluded []string
	pos := 0
	for {
		kind := z.Next()
		if kind == htmltoken.ErrorToken {
			if errors.Is(z.Err(), io.EOF) {
				return regions, nil
			}
			return nil, fmt.Errorf("ruby candidate markup: %w", z.Err())
		}
		raw := string(z.Raw())
		start := pos
		pos += len(raw)
		switch kind {
		case htmltoken.StartTagToken:
			name := localTagName(z.Token().Data)
			if len(excluded) > 0 || excludedRubyRegion(name) {
				excluded = append(excluded, name)
			}
		case htmltoken.EndTagToken:
			name := localTagName(z.Token().Data)
			for i := len(excluded) - 1; i >= 0; i-- {
				if excluded[i] == name {
					excluded = excluded[:i]
					break
				}
			}
		case htmltoken.TextToken:
			if len(excluded) != 0 || raw == "" {
				continue
			}
			region, err := decodeRegion(raw, start, xmlMode)
			if err != nil {
				return nil, err
			}
			if region.Text != "" {
				regions = append(regions, region)
			}
		}
	}
}

func localTagName(name string) string {
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func excludedRubyRegion(name string) bool {
	switch name {
	case "ruby", "script", "style", "iframe", "xmp", "noembed", "noframes":
		return true
	default:
		return false
	}
}

func decodeRegion(raw string, rawStart int, xmlMode bool) (TranslationRegion, error) {
	var b strings.Builder
	var mappings []RegionMapping
	appendPiece := func(text string, start, end int, linear bool) {
		if text == "" {
			return
		}
		viewStart := b.Len()
		b.WriteString(text)
		if linear && len(mappings) > 0 {
			last := &mappings[len(mappings)-1]
			if last.Linear && last.RawEnd == rawStart+start {
				last.ViewEnd = b.Len()
				last.RawEnd = rawStart + end
				return
			}
		}
		mappings = append(mappings, RegionMapping{ViewStart: viewStart, ViewEnd: b.Len(), RawStart: rawStart + start, RawEnd: rawStart + end, Linear: linear})
	}
	for i := 0; i < len(raw); {
		if raw[i] == '\r' {
			end := i + 1
			if end < len(raw) && raw[end] == '\n' {
				end++
			}
			appendPiece("\n", i, end, false)
			i = end
			continue
		}
		if raw[i] == '&' {
			if !xmlMode {
				if decoded, consumed := decodeHTMLRegionEntity(raw[i:]); consumed > 0 {
					appendPiece(decoded, i, i+consumed, false)
					i += consumed
					continue
				}
			}
			if end := strings.IndexByte(raw[i:], ';'); xmlMode && end >= 0 && end <= 64 {
				end += i + 1
				encoded := raw[i:end]
				decoded := html.UnescapeString(encoded)
				if xmlMode {
					var value string
					if err := xml.Unmarshal([]byte("<v>"+encoded+"</v>"), &value); err != nil {
						return TranslationRegion{}, fmt.Errorf("ruby entity: %w", err)
					}
					decoded = value
				}
				if decoded != encoded {
					appendPiece(decoded, i, end, false)
					i = end
					continue
				}
			}
		}
		_, size := utf8.DecodeRuneInString(raw[i:])
		appendPiece(raw[i:i+size], i, i+size, true)
		i += size
	}
	return TranslationRegion{Text: b.String(), Mappings: mappings}, nil
}

// HTML accepts some named and numeric references without a semicolon. Find the
// consumed prefix rather than leaving a decoded entity's raw spelling matchable
// or making an ordinary suffix part of the atomic entity mapping.
func decodeHTMLRegionEntity(raw string) (string, int) {
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
	decoded := html.UnescapeString(encoded)
	if decoded == encoded {
		return "", 0
	}
	rawEnd, decodedEnd := len(encoded), len(decoded)
	for rawEnd > 1 && decodedEnd > 1 && encoded[rawEnd-1] == decoded[decodedEnd-1] {
		rawEnd--
		decodedEnd--
	}
	if html.UnescapeString(encoded[:rawEnd]) != decoded[:decodedEnd] {
		return decoded, len(encoded)
	}
	return decoded[:decodedEnd], rawEnd
}

func (r TranslationRegion) rawBoundary(view int) (int, bool) {
	if view < 0 || view > len(r.Text) || (view < len(r.Text) && !utf8.RuneStart(r.Text[view])) {
		return 0, false
	}
	for _, m := range r.Mappings {
		if view < m.ViewStart || view > m.ViewEnd {
			continue
		}
		if view == m.ViewStart {
			return m.RawStart, true
		}
		if view == m.ViewEnd {
			return m.RawEnd, true
		}
		if m.Linear {
			return m.RawStart + view - m.ViewStart, true
		}
		return 0, false
	}
	return 0, false
}
