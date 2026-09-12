package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service/segmatch"
)

type SegmentService struct {
	client            *ent.Client
	projects          *ProjectService
	dialect           string
	revisionRetention time.Duration
	logger            *slog.Logger
}

type ResourceSegmentPage struct {
	Items         []*ent.Segment
	PrevCursor    int
	HasPrevCursor bool
	NextCursor    int
	HasNextCursor bool
	Total         *int // 仅 IncludeTotal=true 时填充；nil 表示未请求总数
}

type ResourceSegmentListOptions struct {
	AfterID         int
	HasCursor       bool   // 请求是否显式携带 cursor（区分 cursor=0 与未传游标）
	AnchorSegmentID *int   // 数据库 segment ID 锚点：窗口从其 segment_index 开始
	Direction       string // ""/asc（默认升序）/desc
	Limit           int
	Status          string
	Search          string
	SearchField     string // ""/"both"（默认，行为不变）/"source"/"target"
	MatchMode       string // "substring"（默认，空值同）/"regex"
	CaseSensitive   *bool  // nil/true（默认）大小写敏感；false 大小写不敏感
	WholeWord       *bool  // nil（默认）不整词匹配；true 仅匹配独立单词
	IncludeTotal    bool
	GroupKey        string
	QualityIssues   string
	QualitySeverity string
	QualityCode     string
}

type ResourceSegmentUpdateInput struct {
	SourceText *string
	TargetText *string
	Comment    *string
}

type qaBatchInput struct {
	Index      int
	SourceText string
	TargetText string
	OldTarget  string
	Meta       *string
}

func NewSegmentService(client *ent.Client, projects *ProjectService, dialectName string, revisionRetention time.Duration, logger *slog.Logger) *SegmentService {
	if logger == nil {
		logger = slog.Default()
	}
	if revisionRetention <= 0 {
		revisionRetention = 90 * 24 * time.Hour
	}
	return &SegmentService{client: client, projects: projects, dialect: dialectName, revisionRetention: revisionRetention, logger: logger}
}

// applySegmentFilters 把资源范围与 status/quality 过滤应用到查询。
// 列表、扫描批次与 include_total 计数必须共享同一套基础过滤条件，避免两处拼装漂移。
// search 不在此处理：命中判定以 segmatch 的 Go 匹配为最终权威，数据库最多通过
// segmentSearchCandidate 做安全候选粗筛。
func applySegmentFilters(q *ent.SegmentQuery, resourceID int, opts ResourceSegmentListOptions, dialectName string) *ent.SegmentQuery {
	q = q.Where(segment.ResourceIDEQ(resourceID))
	if opts.Status != "" {
		q = q.Where(segment.StatusEQ(segment.Status(opts.Status)))
	}
	if p := buildQualityPredicate(opts, dialectName); p != nil {
		q = q.Where(p)
	}
	return q
}

// ErrSegmentGroupMismatch 表示锚点段落不属于请求的 group_key 章节，
// 无法以其在筛选序列中的位置作为窗口起点。
var ErrSegmentGroupMismatch = errors.New("segment group mismatch")

// ErrDuplicateSegmentIndex 表示同一 resource 下存在重复的 segment_index——数据
// 已损坏。对外分页游标只能表达 segment_index：一旦重复恰好跨越当前页边界
// （页尾之后、或页首之前的同 index 匹配无法用游标再次到达），列表必须报错
// 拒绝返回，而不是静默漏行或返回不可遍历的空页。检测仅发生在取批边界
// （Limit+1 截断行 / 扫描窗口越过边界），正常唯一索引数据零额外开销。
var ErrDuplicateSegmentIndex = errors.New("duplicate segment_index in resource")

// ErrSegmentHydrationIncomplete 表示瘦行扫描选出的页面行在完整行回填时无法
// 原样恢复：同一查询序列内这些行不应消失或改变身份，出现即内部不变量破坏
// （如并发删除/改写），必须明确报错，而不是返回字段残缺或漂移的响应。
// 判定覆盖三种漂移：完整行查不到（含被回填查询的资源/筛选限定挡掉的外资源行）、
// segment_index 与扫描时不一致、以及再次经同一精确过滤器后不再命中。
var ErrSegmentHydrationIncomplete = errors.New("segment hydration incomplete")

// segmentGroupKey 解析 segment meta JSON 中的 epub_file 章节键。
// meta 缺失、非法 JSON 或无有效 epub_file 时返回 ("", false)。
func segmentGroupKey(meta *string) (string, bool) {
	if meta == nil {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(*meta), &m); err != nil {
		return "", false
	}
	key, ok := m["epub_file"].(string)
	if !ok || key == "" {
		return "", false
	}
	return key, true
}

// segmentSearchCandidate 在 matcher 提供非空安全字面量时构造数据库粗筛谓词。
// 字面量必然出现在每个命中段的文本里，粗筛只会放大候选（LIKE 通配符语义等带来
// 的假阳性），最终由 segmatch 的精确匹配纠正；粗筛不得缩小结果集。
// SearchField source/target/""/"both"（默认）；matchMode "substring"（默认）/​"regex"。
//
// SQLite 使用二进制 INSTR 粗筛：INSTR(CAST(column AS BLOB), ?) > 0。参数以
// []byte 字面量经 sql.Builder.Arg 绑定为 BLOB（blob 间按字节比较，不受文本
// 编码影响），因此 LIKE 在列值内嵌 NUL 处截断的问题不复存在，字面量本身含
// NUL 也可安全绑定；%/_/\ 等只是普通字节，不会解释为通配符。
// 但 INSTR 粗筛只对 substring 启用：SQLite 的 TEXT 列可存非法 UTF-8 字节，
// Go regexp 会把非法字节视为 U+FFFD——regex 命中可以完全建立在 U+FFFD 上
// （如 \x{FFFD}），其字面前缀字节在列中并不存在，BLOB INSTR 前缀粗筛会漏掉
// 这类命中，因此 regex 即便有 LiteralPrefix 也必须禁用粗筛、全量精筛。
// PostgreSQL text 列保证合法 UTF-8，substring 与 regex 均可 LIKE 粗筛；但
// 字面量本身含 NUL 时无法作为 text 参数绑定，退回无粗筛的全量精筛。其它
// dialect 保持 LIKE 语义并在字面量含 NUL 时同样退回。
func segmentSearchCandidate(matcher segmatch.Matcher, field, matchMode, dialectName string) predicate.Segment {
	if matcher == nil {
		return nil
	}
	literal := matcher.CandidateLiteral()
	if literal == "" {
		return nil
	}
	if matchMode == "regex" && dialectName == dialect.SQLite {
		return nil
	}
	if dialectName == dialect.SQLite {
		return segmentSearchCandidateInstr(field, literal)
	}
	if strings.IndexByte(literal, 0) >= 0 {
		return nil
	}
	source := segment.SourceTextContains(literal)
	target := segment.TargetTextContains(literal)
	switch field {
	case "source":
		return source
	case "target":
		return target
	default: // "" / "both"
		return segment.Or(source, target)
	}
}

// segmentSearchCandidateInstr 按 field 组合 source_text / target_text 的二进制
// INSTR 谓词（SQLite 专用）。
func segmentSearchCandidateInstr(field, literal string) predicate.Segment {
	source := segmentTextInstr(segment.FieldSourceText, literal)
	target := segmentTextInstr(segment.FieldTargetText, literal)
	switch field {
	case "source":
		return source
	case "target":
		return target
	default: // "" / "both"
		return segment.Or(source, target)
	}
}

// segmentTextInstr 构造二进制包含谓词：INSTR(CAST(col AS BLOB), ?) > 0。
// 字面量以 []byte 经 Builder.Arg 绑定为 BLOB，绝不内联进 SQL 模板；
// CAST 保证比较按字节进行，规避 LIKE/文本函数在内嵌 NUL 处截断的行为。
// 表达式无顶层布尔运算符，与其它谓词 AND 组合无需额外括号；经 segment.Or
// 组合时由 ent 自动包裹。
func segmentTextInstr(field, literal string) predicate.Segment {
	return func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.WriteString("INSTR(CAST(")
			b.Ident(s.C(field))
			b.WriteString(" AS BLOB), ")
			b.Arg([]byte(literal))
			b.WriteString(") > 0")
		}))
	}
}

// segmentMatchFilter 返回行级后过滤：搜索命中与 group_key 章节（meta.epub_file）
// 同时满足。matcher 为 nil 表示无搜索（仅 group_key 过滤）。
func segmentMatchFilter(matcher segmatch.Matcher, field, groupKey string) func(*ent.Segment) bool {
	return func(seg *ent.Segment) bool {
		if matcher != nil && !segmentSearchHit(matcher, field, seg) {
			return false
		}
		if groupKey != "" {
			if key, ok := segmentGroupKey(seg.Meta); !ok || key != groupKey {
				return false
			}
		}
		return true
	}
}

// segmentSearchHit 判定单个段落是否命中搜索。SearchField source/target/both（默认）；
// target/both 下 target 为 nil 的段落不命中。列表命中只关心布尔结果，走
// HasMatch，避免 regex 整词候选为否定结果生成全部匹配。
func segmentSearchHit(matcher segmatch.Matcher, field string, seg *ent.Segment) bool {
	switch field {
	case "source":
		return matcher.HasMatch(seg.SourceText)
	case "target":
		return seg.TargetText != nil && matcher.HasMatch(*seg.TargetText)
	default: // "" / "both"
		if matcher.HasMatch(seg.SourceText) {
			return true
		}
		return seg.TargetText != nil && matcher.HasMatch(*seg.TargetText)
	}
}

// segmentScanBatchSize 计算第 n 批（从 0 起）的扫描批次大小：初始
// clamp(limit*4, 128, 1000)，持续低命中时逐批倍增但不超过 1000。
// 提取为小函数以便测试按需构造跨批次场景，无需生产配置开关。
func segmentScanBatchSize(limit, n int) int {
	size := limit * 4
	if size < 128 {
		size = 128
	}
	if size > 1000 {
		size = 1000
	}
	for i := 0; i < n; i++ {
		size *= 2
		if size > 1000 {
			return 1000
		}
	}
	return size
}

// scanWindow 描述一次取批的 keyset 起点。初始窗口只按 segment_index 过滤
// （对外 cursor/anchor 语义，不因内部续扫键改变）；续扫窗口改用 (index, id)
// 元组——segment_index 不唯一，仅按 index 续扫会在同 index 行跨越批次边界时漏行。
type scanWindow struct {
	from   int  // asc: index > from（gte 时 index >= from）；desc: index < from
	fromID int  // >0 时启用元组续扫：(index>from) OR (index=from AND id>fromID)，desc 反向
	gte    bool // 锚点窗口：index >= from（锚点本身不要求匹配）
	asc0   bool // 无游标无锚点：从最小 index 起
}

// segmentScanFields 是扫描/探测/计数阶段所需的最小列集合。keyset 推进只读
// (segment_index, id)，后过滤只读 source_text / target_text / meta，因此取批
// 不搬运 status、quality_issues、时间戳等大列，也不预加载任何边；完整的行与
// reviewed_by 由 hydrateSegments 在窗口确定后按最终 ID 一次性回填。
var segmentScanFields = []string{
	segment.FieldID,
	segment.FieldSegmentIndex,
	segment.FieldSourceText,
	segment.FieldTargetText,
	segment.FieldMeta,
}

// segmentScanner 以自适应批次沿 (segment_index, id) keyset 扫描候选，按
// segmentMatchFilter 精确过滤。所有取批都不能持有事务。取批仅返回瘦行
// （segmentScanFields），调用方在窗口与 prev/next 确定后统一回填完整行。
type segmentScanner struct {
	baseQ  func() *ent.SegmentQuery // 基础过滤 + 候选粗筛，不含窗口与 Limit
	filter func(*ent.Segment) bool  // 行级精确后过滤（搜索 + group_key）
	limit  int
}

// fetchAsc 升序取一批候选（不含后过滤），按 (segment_index, id) 升序返回。
func (sc *segmentScanner) fetchAsc(ctx context.Context, w scanWindow, size int) ([]*ent.Segment, error) {
	q := sc.baseQ()
	switch {
	case w.gte:
		q = q.Where(segment.SegmentIndexGTE(w.from))
	case w.fromID > 0:
		q = q.Where(segment.Or(
			segment.SegmentIndexGT(w.from),
			segment.And(segment.SegmentIndexEQ(w.from), segment.IDGT(w.fromID)),
		))
	case !w.asc0:
		q = q.Where(segment.SegmentIndexGT(w.from))
	}
	return q.Order(ent.Asc(segment.FieldSegmentIndex), ent.Asc(segment.FieldID)).
		Limit(size).Select(segmentScanFields...).All(ctx)
}

// fetchDesc 降序取一批候选（不含后过滤），按 (segment_index, id) 降序返回。
func (sc *segmentScanner) fetchDesc(ctx context.Context, w scanWindow, size int) ([]*ent.Segment, error) {
	q := sc.baseQ()
	switch {
	case w.fromID > 0:
		q = q.Where(segment.Or(
			segment.SegmentIndexLT(w.from),
			segment.And(segment.SegmentIndexEQ(w.from), segment.IDLT(w.fromID)),
		))
	default:
		q = q.Where(segment.SegmentIndexLT(w.from))
	}
	return q.Order(ent.Desc(segment.FieldSegmentIndex), ent.Desc(segment.FieldID)).
		Limit(size).Select(segmentScanFields...).All(ctx)
}

// ascScanResult 是一次升序窗口收集的结果。
type ascScanResult struct {
	items         []*ent.Segment // 升序
	firstExamined int            // 检查过的最小 index（其下未检查，prev 探测边界）
	lastExamined  int            // 检查过的最大 index（其上未检查，next 探测边界）
	exhausted     bool           // 候选耗尽（某批不满或空）
	matchAfter    bool           // 收满 limit 后仍命中且 index 高于末条匹配（next 直接证据）
}

// collectAsc 升序扫描收集至多 limit 条匹配，每批完整消费后 | 才判定终点，
// 保证 lastExamined 之上没有"取到但未检查"的行，探测边界因此安全。
func (sc *segmentScanner) collectAsc(ctx context.Context, w scanWindow) (ascScanResult, error) {
	res := ascScanResult{firstExamined: -1}
	batch := 0
	for {
		size := segmentScanBatchSize(sc.limit, batch)
		rows, err := sc.fetchAsc(ctx, w, size)
		if err != nil {
			return res, err
		}
		if len(rows) == 0 {
			res.exhausted = true
			return res, nil
		}
		batch++
		if res.firstExamined < 0 {
			res.firstExamined = rows[0].SegmentIndex
		}
		res.lastExamined = rows[len(rows)-1].SegmentIndex
		for _, row := range rows {
			if sc.filter(row) {
				if len(res.items) < sc.limit {
					res.items = append(res.items, row)
				} else if row.SegmentIndex == res.items[len(res.items)-1].SegmentIndex {
					// 收满 limit 后仍命中且与末条匹配同 index：next 游标只能
					// 表达 segment_index，这些行沿 next 推进不可达，拒绝静默漏行。
					return res, ErrDuplicateSegmentIndex
				} else {
					res.matchAfter = true
				}
			}
		}
		if len(res.items) >= sc.limit {
			// 窗口未越过末条匹配的 index（末条匹配恰为批尾，或同 index 行跨
			// 批延续）时，边界之上同 index 是否还有匹配尚未判定；继续扫描
			// 直到检查越过边界或耗尽，否则 next 判定会静默漏掉同 index 匹配。
			if res.lastExamined != res.items[len(res.items)-1].SegmentIndex {
				return res, nil
			}
		} else if len(rows) < size {
			res.exhausted = true
			return res, nil
		}
		// 续扫键取批内最后一行的 (index, id) 元组：排序与谓词同序，逐批严格前进。
		last := rows[len(rows)-1]
		w.from = last.SegmentIndex
		w.fromID = last.ID
		w.gte = false
		w.asc0 = false
	}
}

// descScanResult 是一次降序窗口收集的结果（desc 游标窗口 = 游标之前
// index 最大的 limit 条匹配，沿降序最先遇到）。
type descScanResult struct {
	items        []*ent.Segment // 升序（发现序反转后）
	lastExamined int            // 检查过的最小 index（其下未检查，prev 探测边界）
	exhausted    bool           // 候选耗尽
	matchBelow   bool           // 收满 limit 后仍命中且 index 低于末条匹配（prev 直接证据）
}

// collectDesc 从 index<from 起降序扫描收集至多 limit 条匹配，每批完整消费。
func (sc *segmentScanner) collectDesc(ctx context.Context, from int) (descScanResult, error) {
	res := descScanResult{}
	var buf []*ent.Segment // 发现序（降序）
	w := scanWindow{from: from}
	batch := 0
	for {
		size := segmentScanBatchSize(sc.limit, batch)
		rows, err := sc.fetchDesc(ctx, w, size)
		if err != nil {
			return res, err
		}
		if len(rows) == 0 {
			res.exhausted = true
			break
		}
		batch++
		res.lastExamined = rows[len(rows)-1].SegmentIndex
		for _, row := range rows {
			if sc.filter(row) {
				if len(buf) < sc.limit {
					buf = append(buf, row)
				} else if row.SegmentIndex == buf[len(buf)-1].SegmentIndex {
					// 收满 limit 后仍命中且与已收集的最低 index 同 index：
					// prev 游标只能表达 segment_index，这些行沿 prev 推进
					// 不可达，拒绝静默漏行。
					return res, ErrDuplicateSegmentIndex
				} else {
					res.matchBelow = true
				}
			}
		}
		if len(buf) >= sc.limit {
			// 同 collectAsc：窗口未越过已收集的最低 index（发现序末条恰为
			// 批尾，或同 index 行跨批延续）时继续扫描，直到越过边界或耗尽。
			if res.lastExamined != buf[len(buf)-1].SegmentIndex {
				break
			}
		} else if len(rows) < size {
			res.exhausted = true
			break
		}
		last := rows[len(rows)-1]
		w.from = last.SegmentIndex
		w.fromID = last.ID
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	res.items = buf
	return res, nil
}

// hasNeighborBefore 反向探测 index<ref 是否存在匹配：降序取批逐批推进，
// 找首个匹配即停，扫到资源头部为止。探测边界按 index（对外 cursor 只能表达
// segment_index）；批内推进用 (index, id) 元组，避免同 index 跨批漏行。
func (sc *segmentScanner) hasNeighborBefore(ctx context.Context, ref int) (bool, error) {
	w := scanWindow{from: ref}
	batch := 0
	for {
		size := segmentScanBatchSize(sc.limit, batch)
		rows, err := sc.fetchDesc(ctx, w, size)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if sc.filter(row) {
				return true, nil
			}
		}
		if len(rows) < size {
			return false, nil
		}
		batch++
		last := rows[len(rows)-1]
		w.from = last.SegmentIndex
		w.fromID = last.ID
	}
}

// hasNeighborAfter 正向探测 index>ref 是否存在匹配。
func (sc *segmentScanner) hasNeighborAfter(ctx context.Context, ref int) (bool, error) {
	w := scanWindow{from: ref}
	batch := 0
	for {
		size := segmentScanBatchSize(sc.limit, batch)
		rows, err := sc.fetchAsc(ctx, w, size)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if sc.filter(row) {
				return true, nil
			}
		}
		if len(rows) < size {
			return false, nil
		}
		batch++
		last := rows[len(rows)-1]
		w.from = last.SegmentIndex
		w.fromID = last.ID
	}
}

// countAll 精确遍历全部候选计数（IncludeTotal 用），只计数不保留行，
// 内存 O(batch)。计数不受窗口/游标影响，与数据库路径的 Count 语义一致。
func (sc *segmentScanner) countAll(ctx context.Context) (int, error) {
	w := scanWindow{asc0: true}
	batch := 0
	total := 0
	for {
		rows, err := sc.fetchAsc(ctx, w, segmentScanBatchSize(sc.limit, batch))
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			return total, nil
		}
		batch++
		for _, row := range rows {
			if sc.filter(row) {
				total++
			}
		}
		last := rows[len(rows)-1]
		w.from = last.SegmentIndex
		w.fromID = last.ID
		w.asc0 = false
	}
}

// hydrateSegments 以扫描阶段选出的瘦行一次性回填完整 Segment 行与 reviewed_by 边。
// 回填查询复用 baseQ() 新建的完整过滤查询（resource/status/quality 限定 + 安全
// 候选粗筛），叠加 IDIn 与 WithReviewedBy；不 WithResource（resource_id 为默认列）。
// 扫描阶段 fetch 各自调用的 baseQ() 与 Select 只作用于其查询对象，不会污染此处
// 新建的查询。结果按 thin 的 ID 顺序重排。任一 thin 行查不到、segment_index 与
// 扫描时不一致，或再次经同一 filter 后不再命中，均返回 ErrSegmentHydrationIncomplete
// 并附带上下文，不静默返回残缺/漂移行。空切片短路。
func (s *SegmentService) hydrateSegments(ctx context.Context, thin []*ent.Segment, baseQ func() *ent.SegmentQuery, filter func(*ent.Segment) bool) ([]*ent.Segment, error) {
	if len(thin) == 0 {
		return nil, nil
	}
	ids := make([]int, len(thin))
	for i, row := range thin {
		ids[i] = row.ID
	}
	rows, err := baseQ().
		Where(segment.IDIn(ids...)).
		WithReviewedBy().
		All(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*ent.Segment, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	items := make([]*ent.Segment, len(thin))
	for i, want := range thin {
		row, ok := byID[want.ID]
		if !ok {
			return nil, fmt.Errorf("%w: segment %d not found by resource page query", ErrSegmentHydrationIncomplete, want.ID)
		}
		if row.SegmentIndex != want.SegmentIndex {
			return nil, fmt.Errorf("%w: segment %d index drifted: scan=%d hydrate=%d", ErrSegmentHydrationIncomplete, want.ID, want.SegmentIndex, row.SegmentIndex)
		}
		if filter != nil && !filter(row) {
			return nil, fmt.Errorf("%w: segment %d no longer matches page filters", ErrSegmentHydrationIncomplete, want.ID)
		}
		items[i] = row
	}
	return items, nil
}

// listSegmentsScan 走 keyset 分批扫描的搜索/group_key 列表路径。
// 窗口语义与数据库路径一致：锚点从首个 segment_index>=anchorIndex 的行开始
// （锚点本身不要求匹配）；desc 游标窗口取 index<AfterID 的尾端 limit 条匹配；
// asc 游标取 index>AfterID 起的 limit 条；无游标为升序首页。响应 items 始终升序，
// prev/next 以同一后过滤反向探测确认，不基于数据库粗筛的假阳性。
func (s *SegmentService) listSegmentsScan(ctx context.Context, resourceID int, opts ResourceSegmentListOptions, matcher segmatch.Matcher, cursorRequested, descWindow bool, anchorIndex int) (*ResourceSegmentPage, error) {
	sc := &segmentScanner{
		limit:  opts.Limit,
		filter: segmentMatchFilter(matcher, opts.SearchField, opts.GroupKey),
	}
	sc.baseQ = func() *ent.SegmentQuery {
		q := applySegmentFilters(s.client.Segment.Query(), resourceID, opts, s.dialect)
		if p := segmentSearchCandidate(matcher, opts.SearchField, opts.MatchMode, s.dialect); p != nil {
			q = q.Where(p)
		}
		return q
	}

	page := &ResourceSegmentPage{}
	if descWindow {
		res, err := sc.collectDesc(ctx, opts.AfterID)
		if err != nil {
			return nil, err
		}
		page.Items = res.items
		if len(res.items) > 0 {
			// prev：窗口之下（更低 index）是否仍有匹配。
			hasPrev := res.matchBelow
			if !hasPrev && !res.exhausted {
				var berr error
				hasPrev, berr = sc.hasNeighborBefore(ctx, res.lastExamined)
				if berr != nil {
					return nil, berr
				}
			}
			if hasPrev {
				page.PrevCursor = res.items[0].SegmentIndex
				page.HasPrevCursor = true
			}
			// next：窗口之上（含 >= AfterID 区域）是否仍有匹配。探测从最大
			// item 的 index 起（GT），已检查区域均无匹配，不产生假阳性。
			hasNext, aerr := sc.hasNeighborAfter(ctx, res.items[len(res.items)-1].SegmentIndex)
			if aerr != nil {
				return nil, aerr
			}
			if hasNext {
				page.NextCursor = res.items[len(res.items)-1].SegmentIndex
				page.HasNextCursor = true
			}
		}
	} else {
		start := scanWindow{from: anchorIndex}
		switch {
		case opts.AnchorSegmentID != nil:
			start.gte = true
		case cursorRequested:
			start.from = opts.AfterID
		default:
			start.asc0 = true
		}
		res, err := sc.collectAsc(ctx, start)
		if err != nil {
			return nil, err
		}
		page.Items = res.items
		if len(res.items) > 0 {
			// prev：首个检查行之下未检查，探测确认。
			hasPrev, berr := sc.hasNeighborBefore(ctx, res.firstExamined)
			if berr != nil {
				return nil, berr
			}
			if hasPrev {
				page.PrevCursor = res.items[0].SegmentIndex
				page.HasPrevCursor = true
			}
			// next：收满 limit 后同批剩余出现匹配即有；否则未耗尽时探测确认。
			hasNext := res.matchAfter
			if !hasNext && !res.exhausted {
				hasNext, err = sc.hasNeighborAfter(ctx, res.lastExamined)
				if err != nil {
					return nil, err
				}
			}
			if hasNext {
				page.NextCursor = res.items[len(res.items)-1].SegmentIndex
				page.HasNextCursor = true
			}
		}
	}

	if opts.IncludeTotal {
		total, err := sc.countAll(ctx)
		if err != nil {
			return nil, err
		}
		page.Total = &total
	}

	// prev/next 只依赖 SegmentIndex，均已就绪；此时按扫描得到的瘦行一次性回填完整
	// 行与审核人，避免每批搬运完整字段、也避免逐行查询。回填复用同一 baseQ/filter
	// 复核资源范围与精确命中；空页由 hydrateSegments 短路。
	items, err := s.hydrateSegments(ctx, page.Items, sc.baseQ, sc.filter)
	if err != nil {
		return nil, err
	}
	page.Items = items
	return page, nil
}

func (s *SegmentService) ListResourceSegments(ctx context.Context, actorUserID, projectID, resourceID int, opts ResourceSegmentListOptions) (*ResourceSegmentPage, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, false); err != nil {
		return nil, err
	}
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}

	// 非空 search 的构建失败（regex 不可编译、match mode 不支持）在此即拒绝，
	// 错误原样上抛，errors.Is 可识别 segmatch.ErrInvalidPattern / ErrUnsupportedMatchMode。
	var matcher segmatch.Matcher
	if opts.Search != "" {
		m, err := segmatch.NewMatcher(segmatch.Options{
			Find:          opts.Search,
			MatchMode:     opts.MatchMode,
			CaseSensitive: opts.CaseSensitive,
			WholeWord:     opts.WholeWord,
		})
		if err != nil {
			return nil, err
		}
		matcher = m
	}

	// 锚点窗口：以数据库 segment ID 定位起点，只要求存在于该资源下，
	// 不要求锚点本身满足筛选条件 F；group_key 过滤时还要求锚点同属该章节。
	anchorIndex := 0
	if opts.AnchorSegmentID != nil {
		anchor, err := s.client.Segment.Query().Where(segment.IDEQ(*opts.AnchorSegmentID), segment.ResourceIDEQ(resourceID)).Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, ErrSegmentNotFound
			}
			return nil, err
		}
		if opts.GroupKey != "" {
			if key, ok := segmentGroupKey(anchor.Meta); !ok || key != opts.GroupKey {
				return nil, ErrSegmentGroupMismatch
			}
		}
		anchorIndex = anchor.SegmentIndex
	}

	// 有游标：handler 显式带 cursor，或旧调用直接传 AfterID>0 也视为有游标；
	// desc 无游标仍按升序首页返回。
	cursorRequested := opts.HasCursor || opts.AfterID > 0
	descWindow := opts.Direction == "desc" && cursorRequested && opts.AnchorSegmentID == nil

	if matcher == nil && opts.GroupKey == "" {
		return s.listSegmentsSQLPage(ctx, resourceID, opts, cursorRequested, descWindow, anchorIndex)
	}
	return s.listSegmentsScan(ctx, resourceID, opts, matcher, cursorRequested, descWindow, anchorIndex)
}

// checkSegmentPageBoundaryDuplicate 检查超过 limit 的取批是否在同 index 跨越页
// 边界。fetched 以 (segment_index, id) 排序，第 limit+1 行是被页丢弃的一行：
// 升序页中它落在页尾之后（next 方向），降序窗口中它落在页首之前（prev 方向）。
// 若它与页内边界行同 segment_index，则该侧游标（只能表达 index）永远无法再次
// 表达这些行，返回 ErrDuplicateSegmentIndex 而非静默返回不完整页。
// 行数不足 limit+1 时窗口已取尽全部候选，不存在被丢弃的行，无需检测。
func checkSegmentPageBoundaryDuplicate(fetched []*ent.Segment, limit int) error {
	if len(fetched) <= limit {
		return nil
	}
	if fetched[limit].SegmentIndex == fetched[limit-1].SegmentIndex {
		return ErrDuplicateSegmentIndex
	}
	return nil
}

// listSegmentsSQLPage 无 search 与 group_key 时的数据库分页路径。排序带
// (segment_index, id) 决断：正常数据 index 唯一，排序与历史行为一致；重复数据
// 下同 index 行按 id 稳定 contiguous 排列，使 Limit+1 截断行的位置能精确表达
// "重复是否跨越页边界"。
func (s *SegmentService) listSegmentsSQLPage(ctx context.Context, resourceID int, opts ResourceSegmentListOptions, cursorRequested, descWindow bool, anchorIndex int) (*ResourceSegmentPage, error) {
	q := applySegmentFilters(s.client.Segment.Query(), resourceID, opts, s.dialect)

	switch {
	case descWindow:
		q = q.Where(segment.SegmentIndexLT(opts.AfterID))
	case opts.AnchorSegmentID != nil:
		q = q.Where(segment.SegmentIndexGTE(anchorIndex))
	case cursorRequested:
		q = q.Where(segment.SegmentIndexGT(opts.AfterID))
	}

	var rows []*ent.Segment
	if descWindow {
		fetched, err := q.Order(ent.Desc(segment.FieldSegmentIndex), ent.Desc(segment.FieldID)).
			Limit(opts.Limit + 1).WithReviewedBy().All(ctx)
		if err != nil {
			return nil, err
		}
		if err := checkSegmentPageBoundaryDuplicate(fetched, opts.Limit); err != nil {
			return nil, err
		}
		if len(fetched) > opts.Limit {
			fetched = fetched[:opts.Limit]
		}
		// 响应 items 始终按 segment_index 升序返回
		for i, j := 0, len(fetched)-1; i < j; i, j = i+1, j-1 {
			fetched[i], fetched[j] = fetched[j], fetched[i]
		}
		rows = fetched
	} else {
		// 升序取 Limit+1：页尾之后的第一行（按 id 决断的最低同 index 行）用于
		// 判定 next 游标边界是否被重复 index 跨越，正常页返回前再截断。
		fetched, err := q.Order(ent.Asc(segment.FieldSegmentIndex), ent.Asc(segment.FieldID)).
			Limit(opts.Limit + 1).WithReviewedBy().All(ctx)
		if err != nil {
			return nil, err
		}
		if err := checkSegmentPageBoundaryDuplicate(fetched, opts.Limit); err != nil {
			return nil, err
		}
		if len(fetched) > opts.Limit {
			fetched = fetched[:opts.Limit]
		}
		rows = fetched
	}

	page := &ResourceSegmentPage{Items: rows}
	if len(rows) > 0 {
		// 窗口两侧邻接性按完整过滤条件 F 以存在性查询判断，
		// 计数查询独立构造，不能复用已叠加窗口与 Limit 的 q。
		hasBefore, berr := applySegmentFilters(s.client.Segment.Query(), resourceID, opts, s.dialect).
			Where(segment.SegmentIndexLT(rows[0].SegmentIndex)).Exist(ctx)
		if berr != nil {
			return nil, berr
		}
		hasAfter, aerr := applySegmentFilters(s.client.Segment.Query(), resourceID, opts, s.dialect).
			Where(segment.SegmentIndexGT(rows[len(rows)-1].SegmentIndex)).Exist(ctx)
		if aerr != nil {
			return nil, aerr
		}
		if hasBefore {
			page.PrevCursor = rows[0].SegmentIndex
			page.HasPrevCursor = true
		}
		if hasAfter {
			page.NextCursor = rows[len(rows)-1].SegmentIndex
			page.HasNextCursor = true
		}
	}
	if opts.IncludeTotal {
		countQ := applySegmentFilters(s.client.Segment.Query(), resourceID, opts, s.dialect)
		total, cerr := countQ.Count(ctx)
		if cerr != nil {
			return nil, cerr
		}
		page.Total = &total
	}
	return page, nil
}

// rawPred 把原始 SQL 表达式包装为整体括号化的谓词。ent 以 And 组合多个
// Where 谓词时不会为单 fn 的 ExprP 谓词补括号，含顶层 OR 的表达式会因
// SQL 优先级（AND 高于 OR）劫持同查询的其它过滤条件——quality_issues=none
// 曾因此架空 resource_id 过滤，泄漏其它资源/项目的段落。所有原始谓词必须
// 经此入口括号化，禁止直接使用 sql.ExprP。
func rawPred(expr string) *sql.Predicate {
	return sql.ExprP("(" + expr + ")")
}

// buildQualityPredicate 按 quality_issues / quality_severity / quality_code 构造 SQL 谓词。
// 非法枚举值安全降级为不过滤（返回 nil）。severity 与 code 使用独立 EXISTS（AND）。
// 三个维度均只统计"待处理"的 issue：disposition 为 dismissed 的已被用户驳回，
// 不算问题（与 qa.NeedsAction 语义一致）；disposition 缺失（旧数据）视为 pending。
// SQLite 与 PostgreSQL 的 JSON 函数不同，按 dialectName 分支：SQLite 使用 JSON1
// （json_array_length / json_each / json_extract），PostgreSQL 使用 jsonb_*
// （jsonb_typeof / jsonb_array_length / jsonb_array_elements / ->>）。
//
// 注意：不能使用 sql.ExprP("... = ?", val)。ent 的原始表达式（sql.Expr）直接把
// 模板字符串原样输出，不会把 "?" 重新编号为 Postgres 的 "$N"；而 "?" 在 Postgres
// 里是 jsonb 的"键存在"运算符，会导致 "syntax error at or near ")""（SQLSTATE 42601）。
// severity（warning|error）与 code（qa.IsFilterableIssueCode 白名单）均已强校验，
// 直接以单引号字面量内联即可，避免占位符冲突。
// 所有原始表达式经 rawPred 整体括号化，保证与其它谓词 AND 组合时的语义安全。
func buildQualityPredicate(opts ResourceSegmentListOptions, dialectName string) predicate.Segment {
	usePostgres := dialectName == dialect.Postgres
	var preds []predicate.Segment

	switch opts.QualityIssues {
	case "has":
		preds = append(preds, predicate.Segment(func(s *sql.Selector) {
			col := s.C(segment.FieldQualityIssues)
			if usePostgres {
				// 存在至少一个未驳回（disposition 缺失或 != 'dismissed'）的 issue
				s.Where(rawPred(fmt.Sprintf("jsonb_typeof(%s) = 'array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS v WHERE v ->> 'disposition' IS NULL OR v ->> 'disposition' != 'dismissed')", col, col)))
				return
			}
			s.Where(rawPred(fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s) WHERE json_extract(value, '$.disposition') IS NULL OR json_extract(value, '$.disposition') != 'dismissed')", col)))
		}))
	case "none":
		preds = append(preds, predicate.Segment(func(s *sql.Selector) {
			col := s.C(segment.FieldQualityIssues)
			if usePostgres {
				s.Where(rawPred(fmt.Sprintf("%s IS NULL OR jsonb_typeof(%s) != 'array' OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS v WHERE v ->> 'disposition' IS NULL OR v ->> 'disposition' != 'dismissed')", col, col, col)))
				return
			}
			s.Where(rawPred(fmt.Sprintf("%s IS NULL OR NOT EXISTS (SELECT 1 FROM json_each(%s) WHERE json_extract(value, '$.disposition') IS NULL OR json_extract(value, '$.disposition') != 'dismissed')", col, col)))
		}))
	}

	switch opts.QualitySeverity {
	case "warning", "error":
		sev := opts.QualitySeverity
		preds = append(preds, predicate.Segment(func(s *sql.Selector) {
			col := s.C(segment.FieldQualityIssues)
			if usePostgres {
				s.Where(rawPred(
					fmt.Sprintf("jsonb_typeof(%s) = 'array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS v WHERE (v ->> 'disposition' IS NULL OR v ->> 'disposition' != 'dismissed') AND v ->> 'severity' = '%s')", col, col, sev),
				))
				return
			}
			s.Where(rawPred(
				fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s) WHERE (json_extract(value, '$.disposition') IS NULL OR json_extract(value, '$.disposition') != 'dismissed') AND json_extract(value, '$.severity') = '%s')", col, sev),
			))
		}))
	}

	if qa.IsFilterableIssueCode(opts.QualityCode) {
		code := opts.QualityCode
		preds = append(preds, predicate.Segment(func(s *sql.Selector) {
			col := s.C(segment.FieldQualityIssues)
			if usePostgres {
				s.Where(rawPred(
					fmt.Sprintf("jsonb_typeof(%s) = 'array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS v WHERE (v ->> 'disposition' IS NULL OR v ->> 'disposition' != 'dismissed') AND v ->> 'code' = '%s')", col, col, code),
				))
				return
			}
			s.Where(rawPred(
				fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s) WHERE (json_extract(value, '$.disposition') IS NULL OR json_extract(value, '$.disposition') != 'dismissed') AND json_extract(value, '$.code') = '%s')", col, code),
			))
		}))
	}

	switch len(preds) {
	case 0:
		return nil
	case 1:
		return preds[0]
	default:
		return segment.And(preds...)
	}
}

// ErrSegmentMarkupInvalid 表示提交的译文不是 well-formed XML 片段，无法嵌入该格式
// 的导出文档。放行会导致导出时整章降级为原文——因此在写入边界即拒绝。
var ErrSegmentMarkupInvalid = errors.New("segment target markup invalid")

// SegmentMarkupError 携带 markup.ValidateFragment 返回的具体语法错误，
// 供 handler 拼装可读的 problem detail。
type SegmentMarkupError struct{ Err error }

func (e *SegmentMarkupError) Error() string {
	if e.Err == nil {
		return ErrSegmentMarkupInvalid.Error()
	}
	return ErrSegmentMarkupInvalid.Error() + ": " + e.Err.Error()
}

func (e *SegmentMarkupError) Unwrap() error { return ErrSegmentMarkupInvalid }

func (s *SegmentService) UpdateResourceSegment(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, input ResourceSegmentUpdateInput) (*ent.Segment, error) {
	res, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, true)
	if err != nil {
		return nil, err
	}
	current, err := s.client.Segment.Query().Where(segment.IDEQ(segmentID), segment.ResourceIDEQ(resourceID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}

	update := s.client.Segment.UpdateOneID(current.ID)
	changed := false
	sourceChanged := false
	targetChanged := false
	// QA 输入：source/target 取变更后的值（若未变更则用现状），供 targetChanged 时重算。
	newSource := current.SourceText
	var newTarget string

	if input.SourceText != nil {
		source := strings.TrimSpace(*input.SourceText)
		if source == "" {
			return nil, ErrInvalidInput
		}
		update.SetSourceText(source).SetStatus(SegmentStatusPending)
		changed = true
		sourceChanged = true
		newSource = source
	}
	if input.TargetText != nil {
		target := strings.TrimSpace(*input.TargetText)
		if target == "" {
			return nil, ErrInvalidInput
		}
		// 结构守卫只施加于 renderer 会把译文原样嵌入 XML 文档的格式（目前仅 epub）；
		// txt 等格式译文里的 <color=red>、裸 & 都是合法内容，无门禁的校验会误伤。
		// 格式取 res.Format——requireResourceAccess 已经查出了 Resource，不再额外查询。
		// 这里用硬口径而非「相对原文未退化」的宽松口径：交互场景应当立即报错，且用户
		// 最终必须产出可导出的内容——遗留原文含裸 & 时，译文写 &amp; 才是导出需要的形态。
		if markup.RequiresWellFormedTargets(res.Format) {
			if verr := markup.ValidateFragment(target); verr != nil {
				return nil, &SegmentMarkupError{Err: verr}
			}
		}
		update.SetTargetText(target).SetStatus(SegmentStatusEdited).SetReviewedByID(actorUserID)
		changed = true
		targetChanged = true
		newTarget = target
	}
	if input.Comment != nil {
		comment := strings.TrimSpace(*input.Comment)
		if comment == "" {
			update.ClearReviewComment()
		} else {
			update.SetReviewComment(comment)
		}
		changed = true
	}
	if !changed {
		return nil, ErrInvalidInput
	}
	if sourceChanged && !targetChanged {
		// 原文变更使旧译文失效：清空译文与审核信息。
		// 必须在未显式设置 target_text 时执行，否则会与 SetTargetText
		// 同时存在，触发 PostgreSQL "multiple assignments to same column" 错误。
		update.ClearTargetText().ClearReviewedBy()
	}

	// quality_issues 统一处理（避免与 SetTargetText 同列多次赋值）：
	//   - targetChanged：重跑零配置确定性 QA，与旧 issues 对账后用新结果覆盖；
	//   - sourceChanged && !targetChanged：旧译文失效，无译文不跑 QA，清空旧 issues；
	//   - 仅 comment 变更：不触碰 quality_issues，保持现状。
	switch {
	case targetChanged:
		project, perr := s.client.Project.Get(ctx, projectID)
		var freshIssuesByIndex map[int][]qa.QualityIssue
		if perr != nil {
			s.logger.Warn("manual edit QA: load project failed", "projectID", projectID, "error", perr)
		} else {
			freshIssuesByIndex, _ = s.runManualEditQABatch(ctx, project, res, []qaBatchInput{{
				Index:      current.SegmentIndex,
				SourceText: newSource,
				TargetText: newTarget,
				OldTarget:  oldTargetText(current),
				Meta:       current.Meta,
			}})
		}
		issues := freshIssuesByIndex[current.SegmentIndex]
		// 对账：手动编辑改了译文，重跑零配置确定性 QA。同指纹 issue 的裁决
		// （dismissed 等）应继承，避免用户标了"不是问题"的模式在下次编辑后被冲掉。
		// 注意：runManualEditQABatch 只跑 ZeroConfigDeterministicChecks 白名单，不跑
		// length_ratio/术语表/文档级检查，所以旧的非白名单 issues 会被自然清除
		// （这些 checker 未运行，指纹消失）。这是预期行为。
		issues = qa.ReconcileIssues(issues, current.QualityIssues)
		if len(issues) > 0 {
			update.SetQualityIssues(issues)
		} else {
			update.ClearQualityIssues()
		}
	case sourceChanged:
		update.ClearQualityIssues()
	}

	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithResource().Only(ctx)
}

// runManualEditQABatch 对手动编辑的译文批量运行零配置确定性 QA 检查。
//
// 仅跑 ZeroConfigDeterministicChecks 白名单，避免 length_ratio 用默认阈值与正式
// 翻译流程判定矛盾；不加载术语表，也不跑文档级检查。Protected 区不持久化无法
// 重建，依赖它的 checker 退化为基础扫描。
//
// ZeroConfigDeterministicChecks 白名单排除了 duplicate_source_divergence（文档级、
// 需多段输入）等跨段检查，因此 engine.Run 对 N 个 CheckInput 等价于 N 次单段调用，
// 批量不会改变任何单段的 QA 结果。这是批量复用安全性的依据。
//
// 调用方负责在合适的连接上预查 project（单段编辑走 s.client；事务内批量替换走 tx），
// 避免 MaxOpenConns(1) 下事务占用唯一连接后再用 s.client 查询造成的死锁。
// QA 规则执行本身不阻塞编辑：按现有手动编辑约定记录日志并返回 nil issues。
func (s *SegmentService) runManualEditQABatch(ctx context.Context, project *ent.Project, res *ent.Resource, inputs []qaBatchInput) (map[int][]qa.QualityIssue, error) {
	cfg := qa.DefaultConfig()
	cfg.Enabled = true
	cfg.SourceLang = project.SourceLang
	cfg.TargetLang = project.TargetLang
	cfg.Format = res.Format
	cfg.Checks = qa.ZeroConfigDeterministicChecks()
	engine := qa.NewEngine(cfg, s.logger)

	checkInputs := make([]qa.CheckInput, 0, len(inputs))
	for _, input := range inputs {
		var metaMap map[string]any
		if input.Meta != nil {
			if err := json.Unmarshal([]byte(*input.Meta), &metaMap); err != nil {
				s.logger.Warn("manual edit QA: parse meta failed", "segmentIndex", input.Index, "error", err)
			}
		}
		checkInputs = append(checkInputs, qa.CheckInput{
			Index:      input.Index,
			SourceText: input.SourceText,
			TargetText: input.TargetText,
			Meta:       metaMap,
		})
	}

	issuesByIndex := make(map[int][]qa.QualityIssue)
	for _, issue := range engine.Run(ctx, checkInputs) {
		issuesByIndex[issue.SegmentIndex] = append(issuesByIndex[issue.SegmentIndex], issue)
	}
	// DedupIssues 的指纹是 (code, matched_text)，不含段索引；必须先按段分组再
	// 各自去重，否则不同段的同指纹 issue（如相同的双空格触发 repeated_space）
	// 会被整批去重吞掉，违背上方"批量等价于 N 次单段调用"的约定。
	for idx, issues := range issuesByIndex {
		issuesByIndex[idx] = qa.DedupIssues(issues)
	}
	for _, input := range inputs {
		if input.OldTarget == "" {
			continue
		}
		issuesByIndex[input.Index] = append(issuesByIndex[input.Index], qa.RubyTagLossIssues(input.Index, input.OldTarget, input.TargetText)...)
	}
	return issuesByIndex, nil
}

func oldTargetText(seg *ent.Segment) string {
	if seg.TargetText == nil {
		return ""
	}
	return *seg.TargetText
}

func (s *SegmentService) requireResourceAccess(ctx context.Context, actorUserID, projectID, resourceID int, write bool) (*ent.Resource, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, write); err != nil {
		return nil, err
	}
	row, err := s.client.Resource.Query().Where(resource.IDEQ(resourceID), resource.ProjectIDEQ(projectID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrResourceNotFound
		}
		return nil, err
	}
	return row, nil
}

// ResourceSegmentGroup 表示按章节分组的段落统计信息。
type ResourceSegmentGroup struct {
	GroupKey        string `json:"group_key"`
	GroupTitle      string `json:"group_title"`
	SegmentCount    int    `json:"segment_count"`
	TranslatedCount int    `json:"translated_count"`
	ApprovedCount   int    `json:"approved_count"`
}

type segmentGroupEntry struct {
	groupKey   string
	groupTitle string
	minIndex   int
	count      int
	translated int
	approved   int
}

// ListResourceSegmentGroups 按 meta["epub_file"] 将 segments 归为章节组，返回每组的统计信息。
// 非 EPUB 资源会返回一个包含所有 segments 的单一组。
func (s *SegmentService) ListResourceSegmentGroups(ctx context.Context, actorUserID, projectID, resourceID int) ([]ResourceSegmentGroup, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, false); err != nil {
		return nil, err
	}

	rows, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID)).
		Order(ent.Asc(segment.FieldSegmentIndex)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// 按 groupKey 分组
	groupMap := make(map[string]*segmentGroupEntry)
	var groupOrder []string

	translatedStatuses := map[segment.Status]bool{
		SegmentStatusTranslated: true,
		SegmentStatusEdited:     true,
		SegmentStatusApproved:   true,
	}

	for _, row := range rows {
		groupKey := ""
		groupTitle := ""

		if row.Meta != nil {
			var meta map[string]any
			if err := json.Unmarshal([]byte(*row.Meta), &meta); err == nil {
				if v, ok := meta["epub_file"].(string); ok && v != "" {
					groupKey = v
				}
				// 优先使用章节标题，无法提取时回退到书籍标题
				if v, ok := meta["epub_chapter_title"].(string); ok && v != "" {
					groupTitle = v
				} else if v, ok := meta["epub_chapter_title"].(string); ok && v != "" {
					groupTitle = v
				}
			}
		}

		g, exists := groupMap[groupKey]
		if !exists {
			if groupTitle == "" {
				groupTitle = groupKey
			}
			g = &segmentGroupEntry{
				groupKey:   groupKey,
				groupTitle: groupTitle,
				minIndex:   row.SegmentIndex,
			}
			groupMap[groupKey] = g
			groupOrder = append(groupOrder, groupKey)
		}

		g.count++
		if translatedStatuses[row.Status] {
			g.translated++
		}
		if row.Status == SegmentStatusApproved {
			g.approved++
		}
	}

	// 按 minIndex 排序，保持 spine 顺序
	sort.SliceStable(groupOrder, func(i, j int) bool {
		return groupMap[groupOrder[i]].minIndex < groupMap[groupOrder[j]].minIndex
	})

	result := make([]ResourceSegmentGroup, 0, len(groupOrder))
	for _, key := range groupOrder {
		g := groupMap[key]
		result = append(result, ResourceSegmentGroup{
			GroupKey:        g.groupKey,
			GroupTitle:      g.groupTitle,
			SegmentCount:    g.count,
			TranslatedCount: g.translated,
			ApprovedCount:   g.approved,
		})
	}

	return result, nil
}
