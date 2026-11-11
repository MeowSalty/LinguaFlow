package backend

import "strings"

// ReserveXML checks nesting and counts token/attribute records without making
// names, attribute arrays or a DOM. It reserves the encoding/xml decoder's
// complete buffer/token/string capacities before the decoder is constructed.
func ReserveXML(b *BuildBounds, s string) error {
	depth, nodes := 0, int64(8)
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
			nodes++
			continue
		}
		if strings.HasPrefix(s[pos:], "<![CDATA[") {
			end := strings.Index(s[pos+9:], "]]>")
			if end < 0 {
				break
			}
			pos += 9 + end + 3
			nodes++
			continue
		}
		closing := pos+1 < len(s) && s[pos+1] == '/'
		quote := byte(0)
		end := pos + 1
		attributes := int64(0)
		inWord := false
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
			// encoding/xml accepts adjacent quoted attributes without an
			// intervening space. Each equals sign outside a quote is another
			// potential Attr/namespace entry, independently of word boundaries.
			if c == '=' {
				attributes++
			}
			space := c == ' ' || c == '\t' || c == '\n' || c == '\r'
			if space {
				inWord = false
			} else if !inWord {
				attributes++
				inWord = true
			}
		}
		nodes += 2 + attributes
		if nodes > b.nodeLimit {
			return &BuildCapacityError{"XML nodes"}
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
	if err := b.ReserveNodes(nodes, MaxNodeAllocationBytes); err != nil {
		return err
	}
	n := int64(len(s))
	// Decoder bytes.Buffer growth cumulatively <=4n; copied token bytes and
	// attribute/name strings <=2n; bounded syntax-error formatting <=2n.
	for _, copies := range []int64{4, 2, 2} {
		if n > b.textLimit/copies {
			return &BuildCapacityError{"XML text"}
		}
		if err := b.ReserveText(0, copies*n); err != nil {
			return err
		}
	}
	return nil
}
