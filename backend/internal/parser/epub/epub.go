// epub.go 实现 EPUB 电子书格式的 Parser 接口。
//
// Parse 方法将 EPUB 解压后按 spine 顺序遍历 XHTML 文件，
// 提取块级元素的内部 HTML 作为可翻译 Segment。
//
// Render 方法读取原始 EPUB，按 Segment 的 element_path 定位块级元素，
// 替换为译文后重新打包为 EPUB。
package epub

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"path"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// maxEPUBSize 是 EPUB 文件的最大允许大小（100MB）。
const maxEPUBSize = 100 << 20

// Parser 实现 EPUB 格式的解析和渲染。
type Parser struct{}

// New 创建一个新的 EPUB Parser 实例。
func New() *Parser { return &Parser{} }

// Extensions 返回该 parser 处理的文件扩展名。
func (*Parser) Extensions() []string { return []string{".epub"} }

// Parse 将 EPUB 文件解析为 Document，包含按 spine 顺序排列的 Segment 列表。
func (*Parser) Parse(_ context.Context, r io.Reader) (*pipeline.Document, error) {
	// 1. 读取全部字节（添加 100MB 大小限制保护）
	lr := io.LimitReader(r, maxEPUBSize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("epub: read: %w", err)
	}
	if int64(len(data)) > maxEPUBSize {
		return nil, fmt.Errorf("epub: file too large (max %d MB)", maxEPUBSize>>20)
	}

	// 2. 打开 ZIP
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("epub: open zip: %w", err)
	}

	// 3. DRM 检测
	if err := checkDRM(zipReader); err != nil {
		return nil, err
	}

	// 4. 解析 container.xml → 定位 OPF 文件路径
	opfPath, err := findOPFPath(zipReader)
	if err != nil {
		return nil, fmt.Errorf("epub: find opf: %w", err)
	}
	slog.Debug("[epub:parse] opf path", "path", opfPath)

	// 5. 解析 content.opf → 获取 spine 顺序和元数据
	spine, err := parseSpine(zipReader, opfPath)
	if err != nil {
		return nil, fmt.Errorf("epub: parse spine: %w", err)
	}
	slog.Debug("[epub:parse] spine items", "count", len(spine))
	for i, item := range spine {
		slog.Debug("[epub:parse] spine item", "index", i, "id", item.ID, "href", item.Href, "mediaType", item.MediaType)
	}

	// 列出 ZIP 中所有条目路径（诊断用）
	var zipEntryNames []string
	for _, f := range zipReader.File {
		zipEntryNames = append(zipEntryNames, path.Clean(f.Name))
	}
	slog.Debug("[epub:parse] zip entries", "entries", zipEntryNames)

	metadata := extractMetadata(zipReader, opfPath)

	// 6. 从 NCX 文件提取章节标题映射
	ncxTitles := make(map[string]string)
	if ncxPath, ok := findNCXPath(zipReader, opfPath); ok {
		ncxTitles = extractNCXTitles(zipReader, ncxPath)
		slog.Debug("[epub:parse] ncx titles loaded", "count", len(ncxTitles))
	} else {
		slog.Debug("[epub:parse] no ncx file found")
	}

	// 7. 按 spine 顺序遍历 XHTML 文件
	// 先收集 XHTML TOC 标题（从目录文件中的 <a> 链接提取）
	xhtmlTOCTitles := make(map[string]string)
	var segments []pipeline.Segment

	// 构建 spine 文件集合，用于后续判断 nav 文件是否已在 spine 中
	spineFileSet := make(map[string]bool)
	for _, item := range spine {
		spineFileSet[path.Clean(item.Href)] = true
	}

	for _, item := range spine {
		if !isXHTML(item.MediaType) {
			slog.Debug("[epub:parse] skip non-XHTML", "id", item.ID, "href", item.Href, "mediaType", item.MediaType)
			continue
		}

		xhtmlData, err := readZipFile(zipReader, item.Href)
		if err != nil {
			slog.Debug("[epub:parse] readZipFile failed", "href", item.Href, "error", err)
			continue // 跳过无法读取的文件
		}

		// 如果是 TOC 文件或 nav 文件，提取其中的章节标题映射
		if isTOCFile(item.Href) || isNavFile(item.Href, xhtmlData) {
			titles := extractXHTMLTOCTitles(xhtmlData, item.Href)
			for k, v := range titles {
				if _, exists := xhtmlTOCTitles[k]; !exists {
					xhtmlTOCTitles[k] = v
				}
			}
			slog.Debug("[epub:parse] xhtml toc titles loaded from", "href", item.Href, "newTitles", len(titles), "totalTitles", len(xhtmlTOCTitles))
		}

		fileSegments, err := extractSegmentsFromXHTML(xhtmlData, item.Href)
		if err != nil {
			slog.Debug("[epub:parse] extractSegmentsFromXHTML failed", "href", item.Href, "error", err)
			continue // 跳过解析失败的文件
		}
		slog.Debug("[epub:parse] parsed file", "href", item.Href, "segments", len(fileSegments))

		// 提取章节标题（优先级从高到低）：
		//  1. 目录文件（TOC）→ 使用固定名称 "Contents"
		//  2. XHTML TOC 文件中的标题（最可靠，从 <a> 链接提取）
		//  3. NCX 目录中的标题
		//  4. XHTML <head> 中的 <title> 标签
		//  5. 正文中第一个 <h1>/<h2>/<h3> 标题
		//  6. 文件名（最终回退）
		chapterTitle := resolveChapterTitle(item.Href, xhtmlData, xhtmlTOCTitles, ncxTitles, metadata.Title)
		slog.Debug("[epub:parse] chapter title", "href", item.Href, "chapterTitle", chapterTitle)

		// 为每个 Segment 补充章节级元数据
		for i := range fileSegments {
			if fileSegments[i].Meta == nil {
				fileSegments[i].Meta = map[string]any{}
			}
			fileSegments[i].Meta["epub_title"] = metadata.Title
			fileSegments[i].Meta["epub_chapter_title"] = chapterTitle
			fileSegments[i].Meta["epub_id"] = item.ID
		}

		segments = append(segments, fileSegments...)
	}

	// 8. 处理不在 spine 中的 EPUB3 导航文件（如 navigation-documents.xhtml）
	navFiles := findNavFiles(zipReader, opfPath)
	for _, nav := range navFiles {
		navHref := path.Clean(nav.Href)
		if spineFileSet[navHref] {
			// 已在 spine 中处理过，提取 TOC 标题
			slog.Debug("[epub:parse] nav file already in spine, skipping duplicate processing", "href", navHref)
			continue
		}

		xhtmlData, err := readZipFile(zipReader, nav.Href)
		if err != nil {
			slog.Debug("[epub:parse] readNavFile failed", "href", nav.Href, "error", err)
			continue
		}

		// 提取导航文件中的章节标题映射
		titles := extractXHTMLTOCTitles(xhtmlData, nav.Href)
		for k, v := range titles {
			if _, exists := xhtmlTOCTitles[k]; !exists {
				xhtmlTOCTitles[k] = v
			}
		}
		slog.Debug("[epub:parse] nav file toc titles loaded", "href", nav.Href, "newTitles", len(titles), "totalTitles", len(xhtmlTOCTitles))

		// 提取导航文件中的可翻译 segments
		fileSegments, err := extractSegmentsFromXHTML(xhtmlData, nav.Href)
		if err != nil {
			slog.Debug("[epub:parse] extractNavSegments failed", "href", nav.Href, "error", err)
			continue
		}

		if len(fileSegments) > 0 {
			chapterTitle := resolveChapterTitle(nav.Href, xhtmlData, xhtmlTOCTitles, ncxTitles, metadata.Title)
			for i := range fileSegments {
				if fileSegments[i].Meta == nil {
					fileSegments[i].Meta = map[string]any{}
				}
				fileSegments[i].Meta["epub_title"] = metadata.Title
				fileSegments[i].Meta["epub_chapter_title"] = chapterTitle
				fileSegments[i].Meta["epub_id"] = nav.ID
			}
			segments = append(segments, fileSegments...)
			slog.Debug("[epub:parse] nav file segments extracted", "href", nav.Href, "segments", len(fileSegments))
		}
	}
	slog.Debug("[epub:parse] total segments", "count", len(segments))

	return &pipeline.Document{
		Segments: segments,
		Format:   "epub",
	}, nil
}

// Render 将翻译后的 Document 渲染回 EPUB 格式。
//
// 读取原始 EPUB，按 Segment 的 element_path 或 content_hash 定位块级元素，
// 替换为译文后重新打包为 EPUB。保持 EPUB ZIP 规范合规（mimetype 在首位且不压缩）。
func (*Parser) Render(_ context.Context, doc *pipeline.Document, original io.Reader, w io.Writer) error {
	// 1. 读取原始 EPUB（添加大小限制保护）
	lr := io.LimitReader(original, maxEPUBSize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return fmt.Errorf("epub: read original: %w", err)
	}
	if int64(len(data)) > maxEPUBSize {
		return fmt.Errorf("epub: file too large (max %d MB)", maxEPUBSize>>20)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("epub: open zip: %w", err)
	}

	// 2. 构建 epub_file → []Segment 映射
	segmentsByFile := groupSegmentsByFile(doc.Segments)

	// 3. 解析 spine 获取需要替换的文件集合
	opfPath, err := findOPFPath(zipReader)
	if err != nil {
		return fmt.Errorf("epub: find opf: %w", err)
	}
	spine, err := parseSpine(zipReader, opfPath)
	if err != nil {
		return fmt.Errorf("epub: parse spine: %w", err)
	}
	spFiles := spineFileSet(spine)

	// 查找 nav 文件集合（用于处理不在 spine 中的导航文件）
	navFiles := findNavFiles(zipReader, opfPath)
	navFileSet := make(map[string]bool, len(navFiles))
	for _, nav := range navFiles {
		navFileSet[path.Clean(nav.Href)] = true
	}

	// 4. 创建输出 ZIP
	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()

	// 诊断日志：输出 segmentsByFile 和 spFiles 的 key 集合
	slog.Debug("[epub:render] spFiles keys", "keys", mapKeys(spFiles))
	slog.Debug("[epub:render] segmentsByFile keys", "keys", mapStringKeys(segmentsByFile))
	for k, segs := range segmentsByFile {
		for _, seg := range segs {
			ep, _ := seg.Meta["element_path"].(string)
			hasTarget := seg.Target != ""
			slog.Debug("[epub:render] segment detail",
				"epub_file", k, "element_path", ep, "hasTarget", hasTarget,
				"source", truncate(seg.Source, 30), "target", truncate(seg.Target, 30))
		}
	}

	for _, file := range zipReader.File {
		// mimetype 必须是第一个条目且不压缩（EPUB 规范要求）
		if file.Name == "mimetype" {
			if err := writeMimetype(zipWriter, file); err != nil {
				return fmt.Errorf("epub: write mimetype: %w", err)
			}
			continue
		}

		filePath := path.Clean(file.Name)
		inSpine := spFiles[filePath]
		inNav := navFileSet[filePath]
		hasSegments := segmentsByFile[filePath] != nil
		if inSpine || inNav {
			slog.Debug("[epub:render] file check", "path", filePath, "inSpine", inSpine, "inNav", inNav, "hasSegments", hasSegments)
		}
		if (spFiles[filePath] || navFileSet[filePath]) && segmentsByFile[filePath] != nil {
			// XHTML 章节 → 解析 DOM → 替换译文 → 序列化
			translated, err := renderXHTML(file, segmentsByFile[filePath])
			if err != nil {
				// 降级：写入原始内容
				slog.Debug("[epub:render] renderXHTML failed, fallback to copy", "file", file.Name, "error", err)
				if cErr := copyZipEntry(zipWriter, file); cErr != nil {
					return fmt.Errorf("epub: copy fallback for %s: %w", file.Name, cErr)
				}
				continue
			}
			slog.Debug("[epub:render] renderXHTML OK", "file", file.Name, "bytes", len(translated))
			if err := writeZipEntry(zipWriter, file.Name, translated, file.Method); err != nil {
				return fmt.Errorf("epub: write translated %s: %w", file.Name, err)
			}
		} else {
			// 非章节文件 → 原样复制
			if (inSpine || inNav) && !hasSegments {
				slog.Debug("[epub:render] file has no segments", "path", filePath, "segmentsByFileKeys", mapStringKeys(segmentsByFile))
			}
			if err := copyZipEntry(zipWriter, file); err != nil {
				return fmt.Errorf("epub: copy %s: %w", file.Name, err)
			}
		}
	}

	return nil
}

// checkDRM 检测 EPUB 是否包含 DRM 保护。
// 如果存在 META-INF/encryption.xml 则返回错误。
func checkDRM(zr *zip.Reader) error {
	for _, f := range zr.File {
		if f.Name == "META-INF/encryption.xml" {
			return fmt.Errorf("epub: DRM protected: this EPUB contains DRM protection and cannot be translated")
		}
	}
	return nil
}

// renderXHTML 对单个 XHTML 文件执行译文替换。
//
// 使用 encoding/xml 的 Token 流式处理，按 element_path 定位目标块级元素，
// 将其子节点替换为译文。采用原始字节直通方式保留原始格式。
func renderXHTML(file *zip.File, segments []pipeline.Segment) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open xhtml %s: %w", file.Name, err)
	}
	defer rc.Close()

	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read xhtml %s: %w", file.Name, err)
	}

	pathReplacements := make(map[string]string)
	for _, seg := range segments {
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		if ep, ok := seg.Meta["element_path"].(string); ok {
			pathReplacements[ep] = target
		}
	}
	slog.Debug("[epub:renderXHTML] processing file", "file", file.Name, "segments", len(segments), "pathReplacements", len(pathReplacements))
	for ep, tgt := range pathReplacements {
		slog.Debug("[epub:renderXHTML] path replacement", "path", ep, "target", truncate(tgt, 50))
	}

	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	var buf bytes.Buffer
	if err := processXMLTokens(raw, decoder, pathReplacements, &buf); err != nil {
		return nil, fmt.Errorf("process xhtml %s: %w", file.Name, err)
	}

	// 安全网: 验证输出是 well-formed XML
	verifier := xml.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		_, err := verifier.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("rendered xhtml %s is not well-formed: %w", file.Name, err)
		}
	}

	return buf.Bytes(), nil
}

// processXMLTokens 使用原始字节直通方式处理 XML Token 流。
//
// 通过 element_path 定位目标块级元素，将其子节点替换为译文。
// 保留非替换部分的原始字节，避免 xml.Encoder 重新序列化导致的格式变化。
func processXMLTokens(raw []byte, decoder *xml.Decoder,
	pathReplacements map[string]string, buf *bytes.Buffer) error {

	pt := newPathTracker()
	var (
		replacing     bool
		replaceTarget string
		replaceDepth  int
	)
	prevOffset := int64(0)

	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("xml token error: %w", err)
		}
		currentOffset := decoder.InputOffset()
		tokenBytes := raw[prevOffset:currentOffset]
		prevOffset = currentOffset

		switch t := tok.(type) {
		case xml.StartElement:
			tag := t.Name.Local

			if replacing {
				replaceDepth++
				continue
			}

			pt.push(tag)
			currentPath := pt.path()

			if target, ok := pathReplacements[currentPath]; ok {
				// 进入替换模式
				replacing = true
				replaceTarget = target
				replaceDepth = 1
				slog.Debug("[epub:processXML] path match", "path", currentPath, "target", truncate(target, 50))
				// 写入开标签原始字节
				buf.Write(tokenBytes)
				continue
			}

			// 非替换: 直通原始字节
			buf.Write(tokenBytes)

		case xml.EndElement:
			if replacing {
				replaceDepth--
				if replaceDepth <= 0 {
					// 写入译文
					buf.WriteString(replaceTarget)
					// 写入闭标签原始字节
					buf.Write(tokenBytes)
					replacing = false
					replaceTarget = ""
					replaceDepth = 0
					pt.pop()
				}
				continue
			}

			buf.Write(tokenBytes)
			pt.pop()

		default:
			if replacing {
				continue
			}
			buf.Write(tokenBytes)
		}
	}
	return nil
}

// groupSegmentsByFile 按 epub_file Meta 字段将 Segment 列表分组。
func groupSegmentsByFile(segments []pipeline.Segment) map[string][]pipeline.Segment {
	m := make(map[string][]pipeline.Segment)
	skipped := 0
	for _, seg := range segments {
		ep, ok := seg.Meta["epub_file"].(string)
		if !ok || ep == "" {
			skipped++
			continue
		}
		m[ep] = append(m[ep], seg)
	}
	if skipped > 0 {
		slog.Debug("[epub:groupSegmentsByFile] segments skipped (no epub_file meta)", "count", skipped)
	}
	slog.Debug("[epub:groupSegmentsByFile] grouped segments", "segmentCount", len(segments)-skipped, "fileCount", len(m))
	return m
}

// spineFileSet 构建 spine 中所有 XHTML 文件路径的集合。
func spineFileSet(spine []SpineItem) map[string]bool {
	set := make(map[string]bool, len(spine))
	for _, item := range spine {
		if isXHTML(item.MediaType) {
			set[path.Clean(item.Href)] = true
		}
	}
	return set
}

// writeMimetype 将 mimetype 条目写入 ZIP 的第一个位置，不压缩。
func writeMimetype(zw *zip.Writer, original *zip.File) error {
	// 读取原始 mimetype 内容
	rc, err := original.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}

	// 创建不压缩的文件头
	header := &zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store, // 不压缩
	}
	header.SetModTime(original.Modified)

	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// copyZipEntry 将 ZIP 条目原样复制到目标 ZIP。
func copyZipEntry(zw *zip.Writer, src *zip.File) error {
	rc, err := src.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	// 使用原始文件头
	w, err := zw.CreateHeader(&src.FileHeader)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rc)
	return err
}

// writeZipEntry 将内容写入 ZIP 条目，使用指定的压缩方法。
func writeZipEntry(zw *zip.Writer, name string, data []byte, method uint16) error {
	header := &zip.FileHeader{
		Name:   name,
		Method: method,
	}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func init() {
	parser.Register("epub", New())
}

// 诊断辅助函数

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func mapStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
