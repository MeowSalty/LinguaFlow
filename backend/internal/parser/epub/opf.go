// Package epub 实现 EPUB 电子书格式的 Parser。
//
// opf.go 提供 OPF（Open Packaging Format）解析辅助功能，
// 包括 container.xml 定位、spine 顺序解析、元数据提取等。
package epub

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
)

// containerXML 表示 META-INF/container.xml 的结构。
type containerXML struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

// opfPackage 表示 content.opf 的完整结构。
type opfPackage struct {
	Metadata struct {
		Title string `xml:"http://purl.org/dc/elements/1.1/ title"`
	} `xml:"metadata"`
	Manifest []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		IDRef  string `xml:"idref,attr"`
		Linear string `xml:"linear,attr"`
	} `xml:"spine>itemref"`
}

// SpineItem 表示 spine 中的一个条目，包含 manifest 信息。
type SpineItem struct {
	Href       string // XHTML 文件在 ZIP 内的相对路径
	MediaType  string // MIME 类型
	ID         string // manifest 中的 id
	Linear     bool   // 是否为 linear（默认 true）
	Properties string // manifest 中的 properties 属性
}

// findOPFPath 从 container.xml 定位 OPF 文件路径。
func findOPFPath(zr *zip.Reader) (string, error) {
	f, err := openZipFile(zr, "META-INF/container.xml")
	if err != nil {
		return "", fmt.Errorf("open container.xml: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read container.xml: %w", err)
	}

	var c containerXML
	if err := xml.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("parse container.xml: %w", err)
	}
	if len(c.Rootfiles) == 0 {
		return "", fmt.Errorf("container.xml: no rootfile found")
	}
	return c.Rootfiles[0].FullPath, nil
}

// parseSpine 解析 content.opf，返回按阅读顺序排列的 spine 条目。
// 每个 SpineItem 包含 manifest 中的 href、media-type 和 id。
func parseSpine(zr *zip.Reader, opfPath string) ([]SpineItem, error) {
	f, err := openZipFile(zr, opfPath)
	if err != nil {
		return nil, fmt.Errorf("open opf: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read opf: %w", err)
	}

	var pkg opfPackage
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("parse opf: %w", err)
	}

	// 构建 id → manifest item 映射
	manifest := make(map[string]struct {
		Href       string
		MediaType  string
		Properties string
	}, len(pkg.Manifest))
	for _, item := range pkg.Manifest {
		manifest[item.ID] = struct {
			Href       string
			MediaType  string
			Properties string
		}{Href: item.Href, MediaType: item.MediaType, Properties: item.Properties}
	}

	// 按 spine 顺序组装
	opfDir := path.Dir(opfPath)
	slog.Debug("[epub:parseSpine] opf location", "opfPath", opfPath, "opfDir", opfDir)
	var spine []SpineItem
	for _, ref := range pkg.Spine {
		m, ok := manifest[ref.IDRef]
		if !ok {
			slog.Debug("[epub:parseSpine] idref not found in manifest", "idref", ref.IDRef)
			continue
		}
		// href 相对于 OPF 所在目录，需拼接为 ZIP 内绝对路径
		href := path.Clean(path.Join(opfDir, m.Href))
		slog.Debug("[epub:parseSpine] resolved spine item", "idref", ref.IDRef, "manifestHref", m.Href, "href", href, "mediaType", m.MediaType)
		linear := true
		if strings.EqualFold(ref.Linear, "no") {
			linear = false
		}
		spine = append(spine, SpineItem{
			Href:       href,
			MediaType:  m.MediaType,
			ID:         ref.IDRef,
			Linear:     linear,
			Properties: m.Properties,
		})
	}
	return spine, nil
}

// epubMetadata 从 OPF 中提取书籍元数据。
type epubMetadata struct {
	Title string
}

// extractMetadata 从 content.opf 中提取 dc:title。
func extractMetadata(zr *zip.Reader, opfPath string) epubMetadata {
	f, err := openZipFile(zr, opfPath)
	if err != nil {
		return epubMetadata{}
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return epubMetadata{}
	}

	var pkg opfPackage
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return epubMetadata{}
	}
	return epubMetadata{Title: pkg.Metadata.Title}
}

// readZipFile 读取 ZIP 内指定路径的文件内容。
// 自动处理路径规范化（去除前导 ./ 和 /）。
func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	f, err := openZipFile(zr, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// openZipFile 打开 ZIP 内指定路径的文件，自动规范化路径。
func openZipFile(zr *zip.Reader, name string) (io.ReadCloser, error) {
	name = path.Clean(name)
	// 去除前导 /
	name = strings.TrimPrefix(name, "/")
	for _, f := range zr.File {
		if path.Clean(f.Name) == name {
			return f.Open()
		}
	}
	// 诊断：列出所有候选路径帮助定位不匹配
	var candidates []string
	for _, f := range zr.File {
		candidates = append(candidates, path.Clean(f.Name))
	}
	slog.Debug("[epub:openZipFile] file not found", "name", name, "candidates", candidates)
	return nil, fmt.Errorf("file not found in zip: %s", name)
}

// isXHTML 判断 MIME 类型是否为 XHTML。
func isXHTML(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "application/xhtml+xml", "text/html":
		return true
	}
	return false
}

// --------------------------------------------------------------------------
// NCX（Navigation Center eXtended）解析
// --------------------------------------------------------------------------

// ncxXML 表示 toc.ncx 的 XML 结构。
type ncxXML struct {
	NavMap ncxNavMap `xml:"navMap"`
}

// ncxNavMap 表示 NCX 中的 <navMap> 元素。
type ncxNavMap struct {
	NavPoints []ncxNavPoint `xml:"navPoint"`
}

// ncxNavPoint 表示 NCX 中的 <navPoint> 元素（支持嵌套）。
type ncxNavPoint struct {
	NavLabel ncxNavLabel   `xml:"navLabel"`
	Content  ncxContent    `xml:"content"`
	Children []ncxNavPoint `xml:"navPoint"` // 嵌套的子 navPoint
}

// ncxNavLabel 表示 NCX 中的 <navLabel> 元素。
type ncxNavLabel struct {
	Text string `xml:"text"`
}

// ncxContent 表示 NCX 中的 <content> 元素。
type ncxContent struct {
	Src string `xml:"src,attr"`
}

// findNCXPath 从 OPF manifest 中查找 NCX 文件路径。
//
// 查找策略：
//  1. media-type 为 "application/x-dtbncx+xml" 的条目
//  2. id 包含 "ncx" 的条目
//  3. href 以 "toc.ncx" 结尾的条目
func findNCXPath(zr *zip.Reader, opfPath string) (string, bool) {
	f, err := openZipFile(zr, opfPath)
	if err != nil {
		return "", false
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", false
	}

	var pkg opfPackage
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return "", false
	}

	opfDir := path.Dir(opfPath)

	for _, item := range pkg.Manifest {
		// 策略1: media-type 匹配
		if strings.EqualFold(item.MediaType, "application/x-dtbncx+xml") {
			href := path.Clean(path.Join(opfDir, item.Href))
			slog.Debug("[epub:findNCXPath] found by media-type", "href", href)
			return href, true
		}
		// 策略2: id 包含 "ncx"
		if strings.Contains(strings.ToLower(item.ID), "ncx") {
			href := path.Clean(path.Join(opfDir, item.Href))
			slog.Debug("[epub:findNCXPath] found by id", "href", href)
			return href, true
		}
		// 策略3: href 以 toc.ncx 结尾
		if strings.HasSuffix(strings.ToLower(item.Href), "toc.ncx") {
			href := path.Clean(path.Join(opfDir, item.Href))
			slog.Debug("[epub:findNCXPath] found by href suffix", "href", href)
			return href, true
		}
	}

	return "", false
}

// extractNCXTitles 解析 NCX 文件，提取每个 navPoint 对应的章节标题。
//
// 返回 map[content_src]title，其中 content_src 是 XHTML 文件相对于 OPF 目录的路径。
// 例如：{"chapter1.xhtml": "第一章 开始", "chapter2.xhtml": "第二章 发展"}
func extractNCXTitles(zr *zip.Reader, ncxPath string) map[string]string {
	ncxData, err := readZipFile(zr, ncxPath)
	if err != nil {
		slog.Debug("[epub:extractNCXTitles] read ncx failed", "path", ncxPath, "error", err)
		return nil
	}

	var ncx ncxXML
	if err := xml.Unmarshal(ncxData, &ncx); err != nil {
		slog.Debug("[epub:extractNCXTitles] parse ncx failed", "path", ncxPath, "error", err)
		return nil
	}

	ncxDir := path.Dir(ncxPath)
	titles := make(map[string]string)
	collectNavPoints(ncx.NavMap.NavPoints, ncxDir, titles)

	slog.Debug("[epub:extractNCXTitles] extracted titles", "count", len(titles))
	for src, title := range titles {
		slog.Debug("[epub:extractNCXTitles] title mapping", "src", src, "title", title)
	}

	return titles
}

// collectNavPoints 递归收集 navPoint 中的章节标题映射。
// 处理嵌套的 navPoint 子节点。
func collectNavPoints(navPoints []ncxNavPoint, ncxDir string, titles map[string]string) {
	for _, np := range navPoints {
		src := np.Content.Src
		title := strings.TrimSpace(np.NavLabel.Text)

		if src != "" && title != "" {
			// 规范化 src：去除 fragment（#锚点），拼接为 ZIP 内绝对路径
			if idx := strings.IndexByte(src, '#'); idx >= 0 {
				src = src[:idx]
			}
			fullSrc := path.Clean(path.Join(ncxDir, src))

			// 仅在尚未有标题映射时设置（保留第一个匹配的标题）
			if _, exists := titles[fullSrc]; !exists {
				titles[fullSrc] = title
				slog.Debug("[epub:collectNavPoints] mapped", "src", fullSrc, "title", title)
			}
		}

		// 递归处理子 navPoint
		if len(np.Children) > 0 {
			collectNavPoints(np.Children, ncxDir, titles)
		}
	}
}

// isTOCFile 检测文件名是否表示目录（Contents）文件。
//
// 检查文件名中是否包含 "toc"（不区分大小写）。
func isTOCFile(filename string) bool {
	base := strings.ToLower(path.Base(filename))
	return strings.Contains(base, "toc")
}

// NavItem 表示 OPF manifest 中的导航文件条目。
type NavItem struct {
	Href       string // XHTML 文件在 ZIP 内的相对路径
	ID         string // manifest 中的 id
	Properties string // manifest 中的 properties 属性
}

// findNavFiles 从 OPF manifest 中查找 EPUB3 导航文件。
//
// 查找策略：
//  1. properties 属性包含 "nav" 的条目（EPUB3 标准方式）
//  2. 文件名包含 "navigation-documents" 或 "nav" 且为 XHTML 类型的条目
func findNavFiles(zr *zip.Reader, opfPath string) []NavItem {
	f, err := openZipFile(zr, opfPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	var pkg opfPackage
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return nil
	}

	opfDir := path.Dir(opfPath)
	var navItems []NavItem
	seen := make(map[string]bool)

	for _, item := range pkg.Manifest {
		if !isXHTML(item.MediaType) {
			continue
		}

		href := path.Clean(path.Join(opfDir, item.Href))

		// 策略 1: properties 包含 "nav"
		if hasNavProperty(item.Properties) {
			if !seen[href] {
				navItems = append(navItems, NavItem{
					Href:       href,
					ID:         item.ID,
					Properties: item.Properties,
				})
				seen[href] = true
				slog.Debug("[epub:findNavFiles] found by properties", "href", href, "id", item.ID)
			}
			continue
		}

		// 策略 2: 文件名匹配
		base := strings.ToLower(path.Base(item.Href))
		if strings.Contains(base, "navigation-documents") || base == "nav.xhtml" || base == "nav.htm" {
			if !seen[href] {
				navItems = append(navItems, NavItem{
					Href:       href,
					ID:         item.ID,
					Properties: item.Properties,
				})
				seen[href] = true
				slog.Debug("[epub:findNavFiles] found by filename", "href", href, "id", item.ID)
			}
		}
	}

	return navItems
}

// hasNavProperty 检查 properties 字符串中是否包含 "nav"。
func hasNavProperty(properties string) bool {
	for _, p := range strings.Fields(properties) {
		if strings.TrimSpace(p) == "nav" {
			return true
		}
	}
	return false
}
