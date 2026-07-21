// Package docx 实现 Microsoft Word OOXML (.docx) 格式的 Parser 接口。
//
// Parse 将 word/document.xml 中的 <w:p> 段落聚合为 Segment，
// 内联语义格式以 HTML 表示，视觉属性记入 Meta extras。
//
// Render 按 element_path 定位段落，保留 <w:p> 外壳与 <w:pPr>，
// 将译文 HTML 重建为 <w:r> 写回，其余 ZIP 条目原样复制。
package docx

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// maxDOCXSize 是 DOCX 文件的最大允许大小（100MB）。
const maxDOCXSize = 100 << 20

// maxDecompressedEntrySize 是单个 ZIP 条目解压后的最大允许字节数。
// 用于防御高压缩率 zip 炸弹：压缩流上限不能阻止解压后内存爆炸。
const maxDecompressedEntrySize = 200 << 20

// documentXMLPath 是主文档在 ZIP 内的路径。
const documentXMLPath = "word/document.xml"

// Parser 实现 DOCX 格式的解析和渲染。
type Parser struct{}

// New 创建一个新的 DOCX Parser 实例。
func New() *Parser { return &Parser{} }

// Extensions 返回该 parser 处理的文件扩展名。
func (*Parser) Extensions() []string { return []string{".docx"} }

// Parse 将 DOCX 文件解析为 Document。
func (*Parser) Parse(_ context.Context, r io.Reader) (*pipeline.Document, error) {
	lr := io.LimitReader(r, maxDOCXSize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("docx: read: %w", err)
	}
	if int64(len(data)) > maxDOCXSize {
		return nil, fmt.Errorf("docx: file too large (max %d MB)", maxDOCXSize>>20)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("docx: open zip: %w", err)
	}

	docData, err := readZipFile(zipReader, documentXMLPath)
	if err != nil {
		return nil, fmt.Errorf("docx: read %s: %w", documentXMLPath, err)
	}

	segments, err := extractSegmentsFromDocumentXML(docData)
	if err != nil {
		return nil, fmt.Errorf("docx: extract: %w", err)
	}
	slog.Debug("[docx:parse] segments", "count", len(segments))

	return &pipeline.Document{
		Segments: segments,
		Format:   "docx",
	}, nil
}

// Render 将翻译后的 Document 渲染回 DOCX 格式。
//
// 仅重写 word/document.xml，其余条目原样复制。
func (*Parser) Render(_ context.Context, doc *pipeline.Document, original io.Reader, w io.Writer) error {
	lr := io.LimitReader(original, maxDOCXSize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return fmt.Errorf("docx: read original: %w", err)
	}
	if int64(len(data)) > maxDOCXSize {
		return fmt.Errorf("docx: file too large (max %d MB)", maxDOCXSize>>20)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("docx: open zip: %w", err)
	}

	segmentsByPath := groupSegmentsByPath(doc.Segments)

	zipWriter := zip.NewWriter(w)

	var writeErr error
	for _, file := range zipReader.File {
		clean := path.Clean(file.Name)
		if clean == documentXMLPath || file.Name == documentXMLPath {
			translated, err := renderDocumentXMLFromFile(file, segmentsByPath)
			if err != nil {
				slog.Error("[docx:render] renderDocumentXML failed, fallback to copy",
					"error", err, "file", file.Name)
				if cErr := copyZipEntry(zipWriter, file); cErr != nil {
					writeErr = fmt.Errorf("docx: copy fallback for %s: %w", file.Name, cErr)
					break
				}
				continue
			}
			if err := writeZipEntry(zipWriter, file.Name, translated, file.Method); err != nil {
				writeErr = fmt.Errorf("docx: write translated %s: %w", file.Name, err)
				break
			}
			continue
		}
		if err := copyZipEntry(zipWriter, file); err != nil {
			writeErr = fmt.Errorf("docx: copy %s: %w", file.Name, err)
			break
		}
	}

	if cErr := zipWriter.Close(); cErr != nil {
		if writeErr == nil {
			writeErr = fmt.Errorf("docx: close zip: %w", cErr)
		}
	}
	return writeErr
}

func renderDocumentXMLFromFile(file *zip.File, segmentsByPath map[string][]pipeline.Segment) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := readBounded(rc)
	if err != nil {
		return nil, err
	}
	return renderDocumentXML(raw, segmentsByPath)
}

func groupSegmentsByPath(segments []pipeline.Segment) map[string][]pipeline.Segment {
	m := make(map[string][]pipeline.Segment)
	for _, seg := range segments {
		ep, ok := seg.Meta["element_path"].(string)
		if !ok || ep == "" {
			continue
		}
		if len(m[ep]) > 0 {
			slog.Warn("[docx:render] duplicate segment for element_path, only first used",
				"element_path", ep, "count", len(m[ep])+1)
		}
		m[ep] = append(m[ep], seg)
	}
	return m
}

func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	clean := path.Clean(name)
	for _, f := range zr.File {
		if path.Clean(f.Name) == clean || f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err := readBounded(rc)
			rc.Close()
			return data, err
		}
	}
	return nil, fmt.Errorf("entry %q not found", name)
}

func copyZipEntry(zw *zip.Writer, src *zip.File) error {
	rc, err := src.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	w, err := zw.CreateHeader(&src.FileHeader)
	if err != nil {
		return err
	}
	lr := io.LimitReader(rc, maxDecompressedEntrySize+1)
	n, err := io.Copy(w, lr)
	if err != nil {
		return err
	}
	if n > maxDecompressedEntrySize {
		return fmt.Errorf("docx: entry %q decompressed size exceeds %d bytes", src.Name, maxDecompressedEntrySize)
	}
	return nil
}

// readBounded 读取 r 的全部内容，但限制解压后不超过 maxDecompressedEntrySize。
// 用于防御 zip 解压炸弹：压缩流上限无法阻止解压后内存爆炸。
func readBounded(r io.Reader) ([]byte, error) {
	lr := io.LimitReader(r, maxDecompressedEntrySize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxDecompressedEntrySize {
		return nil, fmt.Errorf("docx: decompressed entry exceeds %d bytes", maxDecompressedEntrySize)
	}
	return data, nil
}

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
	parser.Register("docx", New())
}
