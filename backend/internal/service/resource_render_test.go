package service

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	// 生产二进制经 engine 包空白导入触发各 parser 的 init 自注册；
	// service 测试需要 epub parser 才能解析/渲染上传的 EPUB。
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/parser/epub"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
)

// buildTestEPUB 在内存中构造一个最小合规的 EPUB ZIP（结构与 parser/epub 的
// 内部测试构造器一致，但此处不能跨包复用其私有 helper）：
//   - mimetype 首条且 zip.Store（EPUB 规范要求）；
//   - META-INF/container.xml 指向 OEBPS/content.opf；
//   - manifest + spine 只含一个章节 OEBPS/chapter1.xhtml；
//   - 章节正文为 paragraphs 逐条包成的 <p> 元素。
func buildTestEPUB(t *testing.T, paragraphs []string) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// mimetype：第一个条目、不压缩
	mw, err := w.CreateHeader(&zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store,
	})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := io.WriteString(mw, "application/epub+zip"); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}

	// META-INF/container.xml
	cw, err := w.Create("META-INF/container.xml")
	if err != nil {
		t.Fatalf("create container.xml: %v", err)
	}
	if _, err := io.WriteString(cw, `<?xml version="1.0" encoding="UTF-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`); err != nil {
		t.Fatalf("write container.xml: %v", err)
	}

	// OEBPS/content.opf
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Test Book</dc:title>
    <dc:identifier id="uid">urn:uuid:12345</dc:identifier>
  </metadata>
  <manifest>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
  </spine>
</package>`
	ow, err := w.Create("OEBPS/content.opf")
	if err != nil {
		t.Fatalf("create content.opf: %v", err)
	}
	if _, err := io.WriteString(ow, opf); err != nil {
		t.Fatalf("write content.opf: %v", err)
	}

	// OEBPS/chapter1.xhtml
	body := make([]string, 0, len(paragraphs))
	for _, p := range paragraphs {
		body = append(body, "<p>"+p+"</p>")
	}
	xhtml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>Chapter</title></head>
<body>
%s
</body>
</html>`, strings.Join(body, "\n"))
	xw, err := w.Create("OEBPS/chapter1.xhtml")
	if err != nil {
		t.Fatalf("create chapter1.xhtml: %v", err)
	}
	if _, err := io.WriteString(xw, xhtml); err != nil {
		t.Fatalf("write chapter1.xhtml: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// createTestEpubResource 直接建一条 format=epub 的资源记录（不走上传路径），
// 供不需要真实原始文件的守卫测试使用。存储路径仅作占位，不会被读取。
func createTestEpubResource(t *testing.T, client *ent.Client, projectID int, path string) *ent.Resource {
	t.Helper()
	r, err := client.Resource.Create().
		SetProjectID(projectID).
		SetPath(path).
		SetFormat("epub").
		SetStoragePath("storage/" + path).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create epub resource: %v", err)
	}
	return r
}

// renderTestSetup 走真实上传路径构造一个带 paragraphs 数量 segment 的 epub 资源：
// 原始 EPUB 落入 fileStore，segments 携带解析期生成的 epub_file / element_path Meta，
// 保证 InspectTargets 与 Render 的定位与生产环境一致。
func renderTestSetup(t *testing.T, paragraphs []string) (*ResourceService, *ent.Client, context.Context, *ent.User, *ent.Project, *ent.Resource) {
	t.Helper()
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "render-user")
	project := createTestProject(t, client, "render-proj", user.ID)

	store, err := filestore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("filestore.NewLocal: %v", err)
	}
	svc := NewResourceService(client, NewProjectService(client, nil), store)

	epubBytes := buildTestEPUB(t, paragraphs)
	results, err := svc.UploadResources(ctx, user.ID, project.ID, []UploadedFile{{
		Filename: "book.epub",
		Size:     int64(len(epubBytes)),
		Reader:   bytes.NewReader(epubBytes),
	}})
	if err != nil {
		t.Fatalf("UploadResources: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("upload results=%d want 1", len(results))
	}
	if results[0].Error != "" || results[0].Resource == nil {
		t.Fatalf("upload failed: action=%q error=%q", results[0].Action, results[0].Error)
	}
	if results[0].Resource.TotalSegments != len(paragraphs) {
		t.Fatalf("total_segments=%d want %d (测试 EPUB 本身未解析出预期段落)",
			results[0].Resource.TotalSegments, len(paragraphs))
	}
	return svc, client, ctx, user, project, results[0].Resource
}

// setSegmentTargets 按 segment_index 直接把译文写进 DB（绕过写入守卫）。
// 渲染预检必须能拦下"落库已久"的坏译文，所以这里刻意不走 UpdateResourceSegment。
// targets 中未出现的索引保持无译文。
func setSegmentTargets(t *testing.T, client *ent.Client, ctx context.Context, resourceID int, targets map[int]string) []*ent.Segment {
	t.Helper()
	rows, err := client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID)).
		Order(ent.Asc(segment.FieldSegmentIndex)).
		All(ctx)
	if err != nil {
		t.Fatalf("query segments: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("resource %d has no segments", resourceID)
	}
	for _, row := range rows {
		target, ok := targets[row.SegmentIndex]
		if !ok || target == "" {
			continue
		}
		if _, err := client.Segment.UpdateOneID(row.ID).SetTargetText(target).Save(ctx); err != nil {
			t.Fatalf("set target for segment %d: %v", row.SegmentIndex, err)
		}
	}
	return rows
}

// readChapterFromEPUB 从渲染输出的 ZIP 中读取指定章节文件内容。
func readChapterFromEPUB(t *testing.T, output []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(output), int64(len(output)))
	if err != nil {
		t.Fatalf("output is not a valid zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	t.Fatalf("output zip missing entry %s", name)
	return ""
}

// TestRenderTranslatedResourceRejectsBrokenTargetMarkup 核心回归：
// 单个段落译文结构损坏（缺 </ruby>）时，导出必须在写出任何字节前整体拒绝，
// 否则 handler 已设置 Content-Disposition，无法再改为错误码；而一旦渲染端降级，
// 用户会下载到整章原文而站点仍显示已翻译。
func TestRenderTranslatedResourceRejectsBrokenTargetMarkup(t *testing.T) {
	svc, client, ctx, user, _, res := renderTestSetup(t, []string{"Level 6.", "Good morning."})
	rows := setSegmentTargets(t, client, ctx, res.ID, map[int]string{
		0: "早上好。",
		1: "「等级６：<ruby>劣化雷神皇<rt>雷瑟</rt>！」", // 缺 </ruby>
	})

	buf := &bytes.Buffer{}
	err := svc.RenderTranslatedResource(ctx, user.ID, res, buf)
	if !errors.Is(err, ErrTargetMarkupInvalid) {
		t.Fatalf("err=%v want ErrTargetMarkupInvalid", err)
	}
	var markupErr *TargetMarkupError
	if !errors.As(err, &markupErr) {
		t.Fatalf("err=%T does not unwrap to *TargetMarkupError", err)
	}
	if len(markupErr.Defects) != 1 {
		t.Fatalf("defects=%d want 1", len(markupErr.Defects))
	}
	if want := strconv.Itoa(rows[1].SegmentIndex); markupErr.Defects[0].SegmentID != want {
		t.Fatalf("defect segment_id=%q want %q", markupErr.Defects[0].SegmentID, want)
	}
	if !strings.Contains(markupErr.Defects[0].Location, "OEBPS/chapter1.xhtml") {
		t.Fatalf("defect location=%q want to contain epub 内文件名", markupErr.Defects[0].Location)
	}
	// 关键前提：writer 上必须零字节，HTTP 层才能干净地返回 409。
	if buf.Len() != 0 {
		t.Fatalf("writer got %d bytes, want 0 (预检必须在写出前拒绝)", buf.Len())
	}
}

// TestRenderTranslatedResourceWithValidTargets 验证全部译文合法时正常渲染：
// 输出是合法 ZIP，且目标章节包含译文文本。
func TestRenderTranslatedResourceWithValidTargets(t *testing.T) {
	svc, client, ctx, user, _, res := renderTestSetup(t, []string{"Level 6.", "Good morning."})
	setSegmentTargets(t, client, ctx, res.ID, map[int]string{
		0: "「等级６：<ruby>劣化雷神皇<rt>雷瑟</rt></ruby>！」",
		1: "早上好。",
	})

	buf := &bytes.Buffer{}
	if err := svc.RenderTranslatedResource(ctx, user.ID, res, buf); err != nil {
		t.Fatalf("RenderTranslatedResource: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("render output is empty")
	}
	chapter := readChapterFromEPUB(t, buf.Bytes(), "OEBPS/chapter1.xhtml")
	if !strings.Contains(chapter, "劣化雷神皇") || !strings.Contains(chapter, "早上好") {
		t.Fatalf("chapter missing translated text: %s", chapter)
	}
}

// TestRenderTranslatedResourceEmptyTargetNotDefect 验证空译文不算缺陷：
// 渲染端对空译文保留原节点，不丢任何译文，预检报出来就是误报。
func TestRenderTranslatedResourceEmptyTargetNotDefect(t *testing.T) {
	svc, client, ctx, user, _, res := renderTestSetup(t, []string{"Level 6.", "Good morning."})
	// 只有 index 0 有译文，index 1 保持空。
	setSegmentTargets(t, client, ctx, res.ID, map[int]string{
		0: "早上好。",
	})

	buf := &bytes.Buffer{}
	if err := svc.RenderTranslatedResource(ctx, user.ID, res, buf); err != nil {
		t.Fatalf("RenderTranslatedResource with empty target: %v", err)
	}
	chapter := readChapterFromEPUB(t, buf.Bytes(), "OEBPS/chapter1.xhtml")
	if !strings.Contains(chapter, "早上好") {
		t.Fatalf("chapter missing translated text: %s", chapter)
	}
	if !strings.Contains(chapter, "Good morning.") {
		t.Fatalf("chapter should retain original content for empty target: %s", chapter)
	}
}
