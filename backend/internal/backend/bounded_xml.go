package backend

import "strings"

// ReserveXML validates explicit input and nesting limits before encoding/xml.
// Its private decoder, buffers and namespace maps are not measurable here and
// do not receive guessed allocation credit.
func ReserveXML(b *BuildBounds, s string) error {
	if err := b.CheckContext(); err != nil {
		return err
	}
	if int64(len(s)) > b.inputBytes+b.outputBytes {
		return &BuildCapacityError{"XML input bytes"}
	}
	depth := 0
	for pos := 0; pos < len(s); {
		i := strings.IndexByte(s[pos:], '<')
		if i < 0 {
			break
		}
		pos += i
		if strings.HasPrefix(s[pos:], "<!--") {
			end := strings.Index(s[pos+4:], "-->")
			if end < 0 {
				break
			}
			pos += 4 + end + 3
			continue
		}
		if strings.HasPrefix(s[pos:], "<![CDATA[") {
			end := strings.Index(s[pos+9:], "]]>")
			if end < 0 {
				break
			}
			pos += 9 + end + 3
			continue
		}
		closing := pos+1 < len(s) && s[pos+1] == '/'
		quote := byte(0)
		end := pos + 1
		for ; end < len(s); end++ {
			c := s[end]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				continue
			}
			if c == '>' {
				break
			}
		}
		if end >= len(s) {
			break
		}
		if closing {
			depth = max(0, depth-1)
		} else if end > pos && s[end-1] != '/' && pos+1 < len(s) && s[pos+1] != '!' && s[pos+1] != '?' {
			depth++
			if depth > MaxBuildDepth {
				return &BuildCapacityError{"XML depth"}
			}
		}
		pos = end + 1
	}
	return nil
}
