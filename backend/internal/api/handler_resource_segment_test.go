package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

// srListRequest 构造带 query string 与 chi path params 的认证 GET 请求，
// 直接调用给定 handler（不经 router），复用 srTestServer 的最小装配。
func srListRequest(s *Server, rawQuery string, u *ent.User, handler http.HandlerFunc, pathParams map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/?"+rawQuery, nil)
	rctx := chi.NewRouteContext()
	for k, v := range pathParams {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = withAuthUser(req, u)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// srSegmentIDByIndex 查询资源下指定 segment_index 的段落 ID。
func srSegmentIDByIndex(t *testing.T, client *ent.Client, resourceID, index int) int {
	t.Helper()
	row, err := client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID), segment.SegmentIndexEQ(index)).
		Only(context.Background())
	if err != nil {
		t.Fatalf("query segment index %d: %v", index, err)
	}
	return row.ID
}

func srProblemTitle(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem body: %v (body=%s)", err, rec.Body.String())
	}
	title, _ := body["title"].(string)
	return title
}

func TestHandler_ListResourceSegmentsOpenAPIParameterError(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b", "c")

	router := HandlerWithOptions(s, ChiServerOptions{
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", err.Error())
		},
	})
	for _, rawQuery := range []string{"anchor_segment_id=abc", "anchor_segment_id=1&anchor_segment_id=2"} {
		req := httptest.NewRequest(http.MethodGet,
			"/projects/"+itoa(projectID)+"/resources/"+itoa(resID)+"/segments?"+rawQuery, nil)
		req = withAuthUser(req, u)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query=%q status=%d want 400, body=%s", rawQuery, rec.Code, rec.Body.String())
		}
		if title := srProblemTitle(t, rec); title != "invalid_query_parameter" {
			t.Fatalf("query=%q problem title=%q want invalid_query_parameter", rawQuery, title)
		}
		if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
			t.Fatalf("query=%q content-type=%q want application/problem+json", rawQuery, contentType)
		}
	}
}

func TestHandler_ListResourceSegmentsInvalidMatchMode400(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b")

	rec := srListRequest(s, "search=a&match_mode=glob", u,
		s.handleListResourceSegments,
		map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if title := srProblemTitle(t, rec); title != "invalid_query_parameter" {
		t.Fatalf("problem title=%q want invalid_query_parameter", title)
	}
}

func TestHandler_ListResourceSegmentsInvalidRegex400(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b")

	rec := srListRequest(s, "search=%5B&match_mode=regex", u,
		s.handleListResourceSegments,
		map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if title := srProblemTitle(t, rec); title != "invalid_query_parameter" {
		t.Fatalf("problem title=%q want invalid_query_parameter", title)
	}
}

func TestHandler_ListResourceSegmentsAnchorWithCursor400(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b", "c", "d", "e", "f")
	anchorID := srSegmentIDByIndex(t, client, resID, 2)

	rec := srListRequest(s,
		"anchor_segment_id="+itoa(anchorID)+"&cursor=2", u,
		s.handleListResourceSegments,
		map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if title := srProblemTitle(t, rec); title != "invalid_query_parameter" {
		t.Fatalf("problem title=%q want invalid_query_parameter", title)
	}
}

func TestHandler_ListResourceSegmentsAnchorWithDesc400(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b", "c", "d", "e", "f")
	anchorID := srSegmentIDByIndex(t, client, resID, 2)

	rec := srListRequest(s,
		"anchor_segment_id="+itoa(anchorID)+"&direction=desc", u,
		s.handleListResourceSegments,
		map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if title := srProblemTitle(t, rec); title != "invalid_query_parameter" {
		t.Fatalf("problem title=%q want invalid_query_parameter", title)
	}
}

func TestHandler_ListResourceSegmentsDirectionSideways400(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b", "c")

	rec := srListRequest(s, "direction=sideways", u,
		s.handleListResourceSegments,
		map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if title := srProblemTitle(t, rec); title != "invalid_query_parameter" {
		t.Fatalf("problem title=%q want invalid_query_parameter", title)
	}
}

func TestHandler_ListResourceSegmentsAnchorInvalid400(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b", "c")

	cases := []struct {
		name  string
		query string
	}{
		{"zero", "anchor_segment_id=0"},
		{"negative", "anchor_segment_id=-1"},
		{"non-numeric", "anchor_segment_id=abc"},
		{"duplicated", "anchor_segment_id=1&anchor_segment_id=2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := srListRequest(s, tc.query, u,
				s.handleListResourceSegments,
				map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
			}
			if title := srProblemTitle(t, rec); title != "invalid_query_parameter" {
				t.Fatalf("problem title=%q want invalid_query_parameter", title)
			}
		})
	}
}

func TestHandler_ListResourceSegmentsAnchorSuccess(t *testing.T) {
	s, client, u := srTestServer(t)
	// 8 条段落，anchor 取 index=2（>=2，避免 cursor=0 被 formatCursor 省略）。
	targets := []string{"t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7"}
	projectID, resID := srSeedResource(t, client, u.ID, targets...)
	anchorID := srSegmentIDByIndex(t, client, resID, 2)
	params := map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)}

	// 第一页：anchor + limit=1，应只含 anchor 段落本身。
	rec := srListRequest(s,
		"anchor_segment_id="+itoa(anchorID)+"&limit=1", u,
		s.handleListResourceSegments, params)
	if rec.Code != http.StatusOK {
		t.Fatalf("anchor list status=%d want 200, body=%s", rec.Code, rec.Body.String())
	}
	var resp segmentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode anchor list: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items=%d want 1", len(resp.Items))
	}
	if resp.Items[0].ID != anchorID {
		t.Fatalf("anchor located id=%d want %d", resp.Items[0].ID, anchorID)
	}
	if resp.Items[0].SegmentIndex != 2 {
		t.Fatalf("anchor segment_index=%d want 2", resp.Items[0].SegmentIndex)
	}
	if resp.PrevCursor != "2" {
		t.Fatalf("prev_cursor=%q want \"2\"", resp.PrevCursor)
	}
	if resp.NextCursor != "2" {
		t.Fatalf("next_cursor=%q want \"2\"", resp.NextCursor)
	}

	// 第二页：用 next_cursor 续翻，items 按 segment_index 升序。
	rec = srListRequest(s, "cursor=2&limit=2", u, s.handleListResourceSegments, params)
	if rec.Code != http.StatusOK {
		t.Fatalf("cursor list status=%d want 200, body=%s", rec.Code, rec.Body.String())
	}
	resp = segmentListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode cursor list: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items=%d want 2", len(resp.Items))
	}
	for i, item := range resp.Items {
		if want := 3 + i; item.SegmentIndex != want {
			t.Fatalf("items[%d].segment_index=%d want %d（升序）", i, item.SegmentIndex, want)
		}
	}
	if resp.PrevCursor != "3" {
		t.Fatalf("prev_cursor=%q want \"3\"", resp.PrevCursor)
	}
	if resp.NextCursor != "4" {
		t.Fatalf("next_cursor=%q want \"4\"", resp.NextCursor)
	}
}

func TestHandler_ListResourceSegmentsAnchorWithGroupKeySuccess(t *testing.T) {
	s, client, u := srTestServer(t)
	ctx := context.Background()
	projectID, resID := srSeedResource(t, client, u.ID, "t0", "t1", "t2", "t3", "t4", "t5")
	// index 0/1 无 meta，index>=2 标注 epub_file 章节，anchor 落在 index=2。
	for i := 2; i < 6; i++ {
		if _, err := client.Segment.Update().
			Where(segment.ResourceIDEQ(resID), segment.SegmentIndexEQ(i)).
			SetMeta(`{"epub_file":"ch1.xhtml"}`).Save(ctx); err != nil {
			t.Fatalf("set meta on segment %d: %v", i, err)
		}
	}
	anchorID := srSegmentIDByIndex(t, client, resID, 2)

	rec := srListRequest(s,
		"anchor_segment_id="+itoa(anchorID)+"&group_key=ch1.xhtml&limit=2", u,
		s.handleListResourceSegments,
		map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200, body=%s", rec.Code, rec.Body.String())
	}
	var resp segmentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items=%d want 2", len(resp.Items))
	}
	for i, item := range resp.Items {
		if item.GroupKey != "ch1.xhtml" {
			t.Fatalf("items[%d].group_key=%q want ch1.xhtml", i, item.GroupKey)
		}
	}
	if resp.Items[0].SegmentIndex != 2 || resp.Items[1].SegmentIndex != 3 {
		t.Fatalf("segment_index=(%d,%d) want (2,3)（升序）",
			resp.Items[0].SegmentIndex, resp.Items[1].SegmentIndex)
	}
	if resp.NextCursor != "3" {
		t.Fatalf("next_cursor=%q want \"3\"", resp.NextCursor)
	}
}

func TestHandler_ListResourceSegmentsAnchorOtherResource404(t *testing.T) {
	s, client, u := srTestServer(t)
	ctx := context.Background()
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b", "c")

	// 同项目下的另一个资源及其段落。
	otherRes, err := client.Resource.Create().
		SetProjectID(projectID).SetPath("chapters/other.txt").SetFormat("txt").
		SetStoragePath("storage/other.txt").Save(ctx)
	if err != nil {
		t.Fatalf("create other resource: %v", err)
	}
	otherSeg, err := client.Segment.Create().
		SetResourceID(otherRes.ID).SetSegmentIndex(0).SetSourceText("src").
		SetStatus(segment.StatusTranslated).Save(ctx)
	if err != nil {
		t.Fatalf("create other segment: %v", err)
	}

	rec := srListRequest(s,
		"anchor_segment_id="+itoa(otherSeg.ID), u,
		s.handleListResourceSegments,
		map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404, body=%s", rec.Code, rec.Body.String())
	}
	if title := srProblemTitle(t, rec); title != "not_found" {
		t.Fatalf("problem title=%q want not_found", title)
	}
}

func TestHandler_ListResourceSegmentsAnchorGroupMismatch400(t *testing.T) {
	s, client, u := srTestServer(t)
	ctx := context.Background()
	projectID, resID := srSeedResource(t, client, u.ID, "a", "b", "c")
	if _, err := client.Segment.Update().
		Where(segment.ResourceIDEQ(resID), segment.SegmentIndexEQ(2)).
		SetMeta(`{"epub_file":"ch1.xhtml"}`).Save(ctx); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	anchorID := srSegmentIDByIndex(t, client, resID, 2)

	rec := srListRequest(s,
		"anchor_segment_id="+itoa(anchorID)+"&group_key=ch2.xhtml", u,
		s.handleListResourceSegments,
		map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if title := srProblemTitle(t, rec); title != "invalid_query_parameter" {
		t.Fatalf("problem title=%q want invalid_query_parameter", title)
	}
}

func TestHandler_ListResourceSegmentsZeroCursorRoundTrip(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "t0", "t1", "t2")
	params := map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)}

	first := srListRequest(s, "limit=1", u, s.handleListResourceSegments, params)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d want 200, body=%s", first.Code, first.Body.String())
	}
	var page segmentListResponse
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SegmentIndex != 0 {
		t.Fatalf("first items=%v want index 0", page.Items)
	}
	if page.NextCursor != "0" {
		t.Fatalf("next_cursor=%q want zero cursor", page.NextCursor)
	}

	second := srListRequest(s, "cursor=0&limit=1", u, s.handleListResourceSegments, params)
	if second.Code != http.StatusOK {
		t.Fatalf("second status=%d want 200, body=%s", second.Code, second.Body.String())
	}
	page = segmentListResponse{}
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SegmentIndex != 1 {
		t.Fatalf("second items=%v want index 1", page.Items)
	}
}

// TestHandler_ListResourceSegmentsSearchKeepsWhitespace 防回归：search 保留
// query 原值不 trim，前后空白对 substring/regex 都有语义（" foo" ≠ "foo"），
// 仅空字符串表示未搜索；其它参数（如 status）仍统一 trim。
func TestHandler_ListResourceSegmentsSearchKeepsWhitespace(t *testing.T) {
	s, client, u := srTestServer(t)
	projectID, resID := srSeedResource(t, client, u.ID, "foo", " foo bar", "foo ")
	params := map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)}

	fetch := func(t *testing.T, rawQuery string) []int {
		t.Helper()
		rec := srListRequest(s, rawQuery, u, s.handleListResourceSegments, params)
		if rec.Code != http.StatusOK {
			t.Fatalf("query=%q status=%d want 200, body=%s", rawQuery, rec.Code, rec.Body.String())
		}
		var resp segmentListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		indexes := make([]int, 0, len(resp.Items))
		for _, item := range resp.Items {
			indexes = append(indexes, item.SegmentIndex)
		}
		return indexes
	}

	// 前导空格：substring 只命中 " foo bar"；若被 trim 则与 search=foo 等同。
	if got := fetch(t, "search=%20foo"); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("search=%%20foo items=%v, want [1]（不得等同于 search=foo）", got)
	}
	if got := fetch(t, "search=foo"); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Fatalf("search=foo items=%v, want [0 1 2]", got)
	}
	// 尾空格 substring：" foo bar" 与 "foo " 都含 "foo "（前者 f-o-o-空格）。
	if got := fetch(t, "search=foo%20"); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("search=foo%%20 items=%v, want [1 2]", got)
	}
	// regex 尾空格 + 锚点：`foo $` 只命中以 "foo " 结尾的段落，尾空格有语义。
	if got := fetch(t, "search=foo%20%24&match_mode=regex"); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("regex search=foo $ items=%v, want [2]", got)
	}
	// regex 锚点：^foo$ 只命中无空白的 "foo"。
	if got := fetch(t, "search=%5Efoo%24&match_mode=regex"); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("regex search=^foo$ items=%v, want [0]", got)
	}
	// 空字符串表示未搜索：返回全部段落。
	if got := fetch(t, "search="); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Fatalf("empty search items=%v, want [0 1 2]", got)
	}
	// 其它参数仍 trim：带空白的 status 正常解析（不 trim 则过滤不到任何状态）。
	if got := fetch(t, "status=%20translated%20&search=foo"); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Fatalf("padded status items=%v, want [0 1 2]（status 应仍被 trim）", got)
	}
}

func TestToSegmentResponseGroupKey(t *testing.T) {
	now := time.Now()

	// 有效 meta：epub_file 提取为 GroupKey，JSON 输出含 group_key。
	meta := `{"epub_file":"chapter-1.xhtml","other":1}`
	resp := toSegmentResponse(&ent.Segment{
		ID: 7, SegmentIndex: 3, SourceText: "src",
		Status: segment.StatusTranslated, Meta: &meta,
		CreatedAt: now, UpdatedAt: now,
	})
	if resp.GroupKey != "chapter-1.xhtml" {
		t.Fatalf("group_key=%q want chapter-1.xhtml", resp.GroupKey)
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"group_key":"chapter-1.xhtml"`) {
		t.Fatalf("json missing group_key: %s", b)
	}

	// nil meta：无 GroupKey、无 Meta。
	resp = toSegmentResponse(&ent.Segment{
		ID: 8, SegmentIndex: 0, SourceText: "src",
		Status: segment.StatusTranslated, Meta: nil,
		CreatedAt: now, UpdatedAt: now,
	})
	if resp.GroupKey != "" || resp.Meta != nil {
		t.Fatalf("nil meta: group_key=%q meta=%v want empty", resp.GroupKey, resp.Meta)
	}

	// 非法 JSON meta：解析失败，GroupKey 与 Meta 均缺省。
	bad := "{not-json"
	resp = toSegmentResponse(&ent.Segment{
		ID: 9, SegmentIndex: 1, SourceText: "src",
		Status: segment.StatusTranslated, Meta: &bad,
		CreatedAt: now, UpdatedAt: now,
	})
	if resp.GroupKey != "" || resp.Meta != nil {
		t.Fatalf("invalid meta: group_key=%q meta=%v want empty", resp.GroupKey, resp.Meta)
	}

	// 合法 JSON 但无 epub_file：GroupKey 缺省，Meta 保留。
	noKey := `{"foo":"bar"}`
	resp = toSegmentResponse(&ent.Segment{
		ID: 10, SegmentIndex: 2, SourceText: "src",
		Status: segment.StatusTranslated, Meta: &noKey,
		CreatedAt: now, UpdatedAt: now,
	})
	if resp.GroupKey != "" {
		t.Fatalf("no epub_file: group_key=%q want empty", resp.GroupKey)
	}
	if resp.Meta == nil || resp.Meta["foo"] != "bar" {
		t.Fatalf("no epub_file: meta=%v want preserved", resp.Meta)
	}
}

func TestToOpenAPISegmentGroupKey(t *testing.T) {
	now := time.Now()
	meta := `{"epub_file":"chapter-2.xhtml"}`
	resp := toOpenAPISegment(&ent.Segment{
		ID: 11, SegmentIndex: 4, SourceText: "src",
		Status: segment.StatusTranslated, Meta: &meta,
		CreatedAt: now, UpdatedAt: now,
	})
	if resp.GroupKey == nil || *resp.GroupKey != "chapter-2.xhtml" {
		t.Fatalf("group_key=%v want chapter-2.xhtml", resp.GroupKey)
	}
}

// itoa 是测试内常用的 int 转字符串捷径。
func itoa(v int) string {
	return strconv.Itoa(v)
}

func TestHandler_ListResourceSegmentsDuplicateIndex409(t *testing.T) {
	s, client, u := srTestServer(t)
	ctx := context.Background()
	projectID, resID := srSeedResource(t, client, u.ID, "t0")

	// 数据损坏：再建 10 行与首行共享 segment_index=0。limit=10 的页面恰好
	// 在该重复 index 上截断，next_cursor 无法表达其后的行。
	for i := 0; i < 10; i++ {
		if _, err := client.Segment.Create().
			SetResourceID(resID).SetSegmentIndex(0).SetSourceText("source").
			SetTargetText("dup").SetStatus(segment.StatusTranslated).Save(ctx); err != nil {
			t.Fatalf("create duplicate segment: %v", err)
		}
	}
	params := map[string]string{"projectId": itoa(projectID), "resourceId": itoa(resID)}

	rec := srListRequest(s, "limit=10", u, s.handleListResourceSegments, params)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409, body=%s", rec.Code, rec.Body.String())
	}
	if title := srProblemTitle(t, rec); title != "duplicate_segment_index" {
		t.Fatalf("problem title=%q want duplicate_segment_index", title)
	}
}
