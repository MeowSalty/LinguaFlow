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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"

	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ziputil"
)

// maxEPUBSize 是 EPUB 文件的最大允许大小（100MB）。
const maxEPUBSize = 100 << 20

// maxDecompressedEntrySize 引用共享的解压尺寸上限，用于需要全量读入内存的解析路径。
// 原样直通的资产复制不走此限制。
const maxDecompressedEntrySize = ziputil.MaxDecompressedEntrySize

// Parser 实现 EPUB 格式的解析和渲染。
type Parser struct{}

// New 创建一个新的 EPUB Parser 实例。
func New() *Parser { return &Parser{} }

// Extensions 返回该 parser 处理的文件扩展名。
func (*Parser) Extensions() []string { return []string{".epub"} }

// Parse 将 EPUB 文件解析为 Document，包含按 spine 顺序排列的 Segment 列表。
func (*Parser) Parse(_ context.Context, r io.Reader, _ string) (*pipeline.Document, error) {
	// 1. 打开 ZIP（优先零拷贝）
	zipReader, err := ziputil.OpenZip(r, maxEPUBSize)
	if err != nil {
		return nil, fmt.Errorf("epub: open zip: %w", err)
	}

	// 2. DRM 检测
	if err := checkDRM(zipReader); err != nil {
		return nil, err
	}

	// 3. 解析 container.xml → 定位 OPF 文件路径
	opfPath, err := findOPFPath(zipReader)
	if err != nil {
		return nil, fmt.Errorf("epub: find opf: %w", err)
	}
	slog.Debug("[epub:parse] opf path", "path", opfPath)

	// 4. 解析 content.opf → 获取 spine 顺序和元数据
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

	// 5. 从 NCX 文件提取章节标题映射
	ncxTitles := make(map[string]string)
	if ncxPath, ok := findNCXPath(zipReader, opfPath); ok {
		ncxTitles = extractNCXTitles(zipReader, ncxPath)
		slog.Debug("[epub:parse] ncx titles loaded", "count", len(ncxTitles))
	} else {
		slog.Debug("[epub:parse] no ncx file found")
	}

	// 6. 按 spine 顺序遍历 XHTML 文件
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

		xhtmlData, err := ziputil.ReadEntry(zipReader, item.Href, maxDecompressedEntrySize)
		if err != nil {
			// 大小超限意味着章节内容会丢失，用 Warn 暴露给运维；
			// 其他读取错误（条目缺失等）保持 Debug，与历史行为一致。
			if isSizeLimitErr(err) {
				slog.Warn("[epub:parse] chapter skipped due to decompressed size limit",
					"href", item.Href, "limit", maxDecompressedEntrySize, "error", err)
			} else {
				slog.Debug("[epub:parse] ReadEntry failed", "href", item.Href, "error", err)
			}
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

	// 7. 处理不在 spine 中的 EPUB3 导航文件（如 navigation-documents.xhtml）
	navFiles := findNavFiles(zipReader, opfPath)
	for _, nav := range navFiles {
		navHref := path.Clean(nav.Href)
		if spineFileSet[navHref] {
			// 已在 spine 中处理过，提取 TOC 标题
			slog.Debug("[epub:parse] nav file already in spine, skipping duplicate processing", "href", navHref)
			continue
		}

		xhtmlData, err := ziputil.ReadEntry(zipReader, nav.Href, maxDecompressedEntrySize)
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
	// 1. 打开原始 EPUB（优先零拷贝）
	zipReader, err := ziputil.OpenZip(original, maxEPUBSize)
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

	var writeErr error
	for _, file := range zipReader.File {
		// mimetype 必须是第一个条目且不压缩（EPUB 规范要求）
		if file.Name == "mimetype" {
			if err := writeMimetype(zipWriter, file); err != nil {
				writeErr = fmt.Errorf("epub: write mimetype: %w", err)
				break
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
				// 整章降级：写入原始内容。该路径意味着整章严格校验失败，读者将拿到
				// 未翻译的原文而站点仍显示已翻译，属于静默数据丢失，必须以 Warn 暴露
				// 给运维（对齐 docx.go 的 Error 级先例）。正常情况下上游预检与单段容错
				// 已把坏译文拦下，此处仅作最后兜底。
				slog.Warn("[epub:render] renderXHTML failed, whole chapter falls back to untranslated copy",
					"file", file.Name, "error", err)
				if cErr := ziputil.CopyEntryUnbounded(zipWriter, file); cErr != nil {
					writeErr = fmt.Errorf("epub: copy fallback for %s: %w", file.Name, cErr)
					break
				}
				continue
			}
			slog.Debug("[epub:render] renderXHTML OK", "file", file.Name, "bytes", len(translated))
			if err := ziputil.WriteEntry(zipWriter, file.Name, translated, file.Method); err != nil {
				writeErr = fmt.Errorf("epub: write translated %s: %w", file.Name, err)
				break
			}
		} else {
			// 非章节文件 → 原样复制（资产不经解析，不解压进内存，无炸弹风险）
			if (inSpine || inNav) && !hasSegments {
				slog.Debug("[epub:render] file has no segments", "path", filePath, "segmentsByFileKeys", mapStringKeys(segmentsByFile))
			}
			if err := ziputil.CopyEntryUnbounded(zipWriter, file); err != nil {
				writeErr = fmt.Errorf("epub: copy %s: %w", file.Name, err)
				break
			}
		}
	}

	if cErr := zipWriter.Close(); cErr != nil {
		if writeErr == nil {
			writeErr = fmt.Errorf("epub: close zip: %w", cErr)
		}
	}
	return writeErr
}

// InspectTargets 在渲染前逐段预检译文片段的 XML 结构合法性。
//
// 判定条件与 renderXHTML 的替换条件同源（groupSegmentsByFile + pathReplacements）：
// 仅考虑同时具备 epub_file 与 element_path 的段落，且只在译文非空但无法通过
// markup.ValidateFragment 时报缺陷——译文为空时渲染端保留原节点内容，不丢任何
// 译文，报了就是误报。与 Render 的降级判定共用 markup.ValidateFragment 这一口径，
// 保证预检为空 ⇔ Render 不丢弃任何译文。
//
// 同一 (epub_file, element_path) 有多段时只看最后一段：renderXHTML 用 map 承载
// 替换关系，后写覆盖前写，被覆盖的段根本不会进入输出，为它报缺陷就是无理由地
// 阻断下载。
func (*Parser) InspectTargets(doc *pipeline.Document) []parser.TargetDefect {
	type slot struct {
		id     string
		target string
	}
	effective := make(map[string]slot)
	order := make([]string, 0, len(doc.Segments))
	for _, seg := range doc.Segments {
		epubFile, _ := seg.Meta["epub_file"].(string)
		elementPath, _ := seg.Meta["element_path"].(string)
		if epubFile == "" || elementPath == "" {
			continue
		}
		key := epubFile + " " + elementPath
		if _, seen := effective[key]; !seen {
			order = append(order, key)
		}
		effective[key] = slot{id: seg.ID, target: seg.Target}
	}

	var defects []parser.TargetDefect
	for _, key := range order {
		s := effective[key]
		if s.target == "" {
			continue
		}
		if err := markup.ValidateFragment(s.target); err != nil {
			defects = append(defects, parser.TargetDefect{
				SegmentID: s.id,
				Location:  key,
				Reason:    err.Error(),
			})
		}
	}
	return defects
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

// isSizeLimitErr 判断是否为解压尺寸超限错误（用于区分“章节被丢弃”与普通读取失败）。
func isSizeLimitErr(err error) bool {
	return errors.Is(err, ziputil.ErrDecompressedSizeExceeded)
}

// renderXHTML 对单个 XHTML 文件执行译文替换。
//
// 使用 encoding/xml 的 Token 流式处理，按 element_path 定位目标块级元素，
// 将其子节点替换为译文。采用原始字节直通方式保留原始格式。
func renderXHTML(file *zip.File, segments []pipeline.Segment) ([]byte, error) {
	raw, err := ziputil.ReadFile(file, maxDecompressedEntrySize)
	if err != nil {
		return nil, fmt.Errorf("read xhtml %s: %w", file.Name, err)
	}

	pathReplacements := make(map[string]string)
	for _, seg := range segments {
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		ep, ok := seg.Meta["element_path"].(string)
		if !ok {
			continue
		}
		if err := markup.ValidateFragment(target); err != nil {
			// 译文结构损坏时直接放弃替换该元素：不写入映射时 processXMLTokens
			// 走非替换直通分支，原始节点字节完整保留，整章仍然合法。不回退写入
			// seg.Source——遗留数据的 source_text 本身也可能非法（提取期 CharData
			// 未转义），写回去照样毒死整章。
			slog.Warn("[epub:renderXHTML] skip segment with invalid markup, keep original content",
				"file", file.Name, "element_path", ep, "segment_id", seg.ID, "error", err)
			continue
		}
		pathReplacements[ep] = target
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
	if verr := wellFormedXML(buf.Bytes()); verr != nil {
		// 原文本身就不是严格合法的 XML 时不回退。最常见的是 XHTML DTD 实体
		// （&nbsp; 等）——浏览器与阅读器靠 DTD 认得它们，Go 的解码器不加载 DTD，
		// 于是把它们判为非法。这类字节位于可提取块级元素之外，会被原样直通到
		// 输出，让整章校验必然失败。此时回退成复制原文换不来更合法的输出，只会
		// 白丢整章译文（正是本函数要防的事故）。保留渲染结果是安全的：每段插入
		// 的译文都已单独通过 markup.ValidateFragment，其余字节原样直通，输出不
		// 会比原文更坏。
		if oerr := wellFormedXML(raw); oerr != nil {
			slog.Warn("[epub:renderXHTML] original xhtml is not strict XML, keeping rendered output anyway",
				"file", file.Name, "original_error", oerr, "rendered_error", verr)
			return buf.Bytes(), nil
		}
		return nil, fmt.Errorf("rendered xhtml %s is not well-formed: %w", file.Name, verr)
	}

	return buf.Bytes(), nil
}

// wellFormedXML 报告 data 是否为严格 well-formed 的 XML。
// 与 markup.ValidateFragment 同口径（默认严格解码器，不开 AutoClose、不注入
// HTML 实体表），因此「每段片段合法 + 原文合法」即可推出「整章合法」。
func wellFormedXML(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
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
	data, err := ziputil.ReadFile(original, maxDecompressedEntrySize)
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
