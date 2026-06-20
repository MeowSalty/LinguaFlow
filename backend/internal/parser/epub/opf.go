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
		ID        string `xml:"id,attr"`
		Href      string `xml:"href,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		IDRef  string `xml:"idref,attr"`
		Linear string `xml:"linear,attr"`
	} `xml:"spine>itemref"`
}

// SpineItem 表示 spine 中的一个条目，包含 manifest 信息。
type SpineItem struct {
	Href      string // XHTML 文件在 ZIP 内的相对路径
	MediaType string // MIME 类型
	ID        string // manifest 中的 id
	Linear    bool   // 是否为 linear（默认 true）
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
		Href      string
		MediaType string
	}, len(pkg.Manifest))
	for _, item := range pkg.Manifest {
		manifest[item.ID] = struct {
			Href      string
			MediaType string
		}{Href: item.Href, MediaType: item.MediaType}
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
			Href:      href,
			MediaType: m.MediaType,
			ID:        ref.IDRef,
			Linear:    linear,
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
