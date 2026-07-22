package docx

import (
	"encoding/xml"
	"strings"
)

// WordprocessingML 主命名空间。
const wNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

// extrasOrder 是 rPr 视觉属性子元素的单一权威顺序。
// visualExtrasProps（捕获门）与 assembleParagraph 的序列化顺序均派生自此，
// 避免两份并行表发散导致 extras 在 Parse→Render 间静默丢失。
var extrasOrder = []string{
	"rFonts", "color", "sz", "szCs", "spacing", "shd",
	"smallCaps", "caps", "lang", "rtl", "kern", "position",
	"bdr", "effect", "emboss", "outline", "shadow", "imprint",
	"vanish", "webHidden", "noProof", "snapToGrid", "cs", "bCs",
	"iCs", "em", "fitText", "w", "eastAsianLayout", "specVanish", "oMath",
	"highlight",
}

// visualExtrasProps 是整体进 extras 的 rPr 子元素本地名集合。
// 原则：多属性元素（color/rFonts/shd 等）绝不拆分，整体进 extras。
var visualExtrasProps = func() map[string]bool {
	m := make(map[string]bool, len(extrasOrder))
	for _, name := range extrasOrder {
		m[name] = true
	}
	return m
}()

// semanticPropOrder 定义语义标签生成的稳定顺序（外→内）。
var semanticPropOrder = []string{
	"rStyle", "b", "i", "u", "strike", "dstrike", "vertAlign",
}

// boolFalseVal 判断布尔型 rPr 元素是否为显式关闭（w:val="0"/"false"）。
func boolFalseVal(el xml.StartElement) bool {
	for _, attr := range el.Attr {
		if attr.Name.Local != "val" {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(attr.Value))
		return v == "0" || v == "false" || v == "off"
	}
	return false
}

// attrVal 返回元素属性本地名对应的值。
func attrVal(el xml.StartElement, local string) string {
	for _, attr := range el.Attr {
		if attr.Name.Local == local {
			return attr.Value
		}
	}
	return ""
}

// isWordNS 判断是否为 WordprocessingML 命名空间。
func isWordNS(space string) bool {
	return space == "" || space == wNS || space == "w"
}

// semanticTagsFromRPr 根据 rPr 子元素生成语义 HTML 开/闭标签序列。
// 返回 openTags（外→内）与 closeTags（内→外）。
func semanticTagsFromRPr(props []xml.StartElement) (openTags, closeTags []string) {
	type semanticInfo struct {
		open  string
		close string
	}
	found := make(map[string]semanticInfo)

	for _, el := range props {
		if !isWordNS(el.Name.Space) {
			continue
		}
		local := el.Name.Local
		switch local {
		case "b":
			if !boolFalseVal(el) {
				found["b"] = semanticInfo{"<b>", "</b>"}
			}
		case "i":
			if !boolFalseVal(el) {
				found["i"] = semanticInfo{"<i>", "</i>"}
			}
		case "u":
			if !boolFalseVal(el) {
				val := strings.ToLower(attrVal(el, "val"))
				if val != "none" && val != "0" && val != "false" {
					found["u"] = semanticInfo{"<u>", "</u>"}
				}
			}
		case "strike", "dstrike":
			if !boolFalseVal(el) {
				found[local] = semanticInfo{"<s>", "</s>"}
			}
		case "vertAlign":
			switch strings.ToLower(attrVal(el, "val")) {
			case "superscript":
				found["vertAlign"] = semanticInfo{"<sup>", "</sup>"}
			case "subscript":
				found["vertAlign"] = semanticInfo{"<sub>", "</sub>"}
			}
		case "rStyle":
			if style := attrVal(el, "val"); style != "" {
				style = xmlEscapeAttr(style)
				found["rStyle"] = semanticInfo{
					open:  `<span class="` + style + `">`,
					close: "</span>",
				}
			}
		}
	}

	seenS := false
	for _, key := range semanticPropOrder {
		var info semanticInfo
		var ok bool
		if key == "strike" || key == "dstrike" {
			if seenS {
				continue
			}
			if info, ok = found["strike"]; !ok {
				info, ok = found["dstrike"]
			}
			if !ok {
				continue
			}
			seenS = true
		} else {
			info, ok = found[key]
			if !ok {
				continue
			}
		}
		openTags = append(openTags, info.open)
		closeTags = append(closeTags, info.close)
	}
	for i, j := 0, len(closeTags)-1; i < j; i, j = i+1, j-1 {
		closeTags[i], closeTags[j] = closeTags[j], closeTags[i]
	}
	return openTags, closeTags
}

// htmlTagToRPrXML 将 HTML 标签名映射为 rPr 子元素 XML。
// classVal 仅对 span 有效。
func htmlTagToRPrXML(tag, classVal string) (string, bool) {
	switch strings.ToLower(tag) {
	case "b", "strong":
		return "<w:b/>", true
	case "i", "em":
		return "<w:i/>", true
	case "u":
		return "<w:u/>", true
	case "s", "del", "strike":
		return "<w:strike/>", true
	case "sup":
		return `<w:vertAlign w:val="superscript"/>`, true
	case "sub":
		return `<w:vertAlign w:val="subscript"/>`, true
	case "span":
		if classVal == "" {
			return "", false
		}
		classVal = xmlEscapeAttr(classVal)
		return `<w:rStyle w:val="` + classVal + `"/>`, true
	default:
		return "", false
	}
}

// isReplaceableRun 判断是否为应被译文替换的 run 级元素（Render 时整体抑制并写入译文）。
func isReplaceableRun(local string) bool {
	return local == "r"
}

// isPreserveWrapper 判断是否为应保留外壳的容器元素（hyperlink/smartTag/sdt）。
// Render 时保留开/闭标签与属性（如 r:id），仅替换其内部 run。
func isPreserveWrapper(local string) bool {
	switch local {
	case "hyperlink", "smartTag", "sdt":
		return true
	default:
		return false
	}
}

// isSkipEmbedded 判断是否为应跳过的嵌入对象。
func isSkipEmbedded(local string) bool {
	switch local {
	case "drawing", "pict", "object", "oleObject":
		return true
	default:
		return false
	}
}
