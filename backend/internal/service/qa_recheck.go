package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// maxRecheckSegments 单次 QA 重检允许的最大选中段落总数。包级 var 以便测试按需调小。
var maxRecheckSegments = 50000

// recheckWriteBatchSize QA 重检写回的短事务批次大小：每批一个事务，避免长事务
// 长时间占用唯一连接（MaxOpenConns(1)）阻塞其他请求。
const recheckWriteBatchSize = 200

var (
	// ErrQAProfileDisabled 表示指定的执行策略未启用 QA（重检依赖策略的 QA 配置）。
	ErrQAProfileDisabled = errors.New("execution profile has QA disabled")
	// ErrRecheckTooLarge 表示重检选中的段落总数超出上限。
	ErrRecheckTooLarge = errors.New("qa recheck selection exceeds segment limit")
)

// QARecheckService QA 重检服务：对项目内选中的资源/段落组/段落，用指定执行策略
// 当前的 QA 配置与保护规则重跑确定性 QA 与文档级 duplicate_source_divergence
// 检查，按指纹对账继承既有裁决后写回 segment.quality_issues。
// 不修改译文、不修改段落状态、不走 job 体系、同步执行。
type QARecheckService struct {
	client   *ent.Client
	projects *ProjectService
	profiles *ExecutionProfileService
	logger   *slog.Logger
}

// NewQARecheckService 创建 QARecheckService 实例。
func NewQARecheckService(client *ent.Client, projects *ProjectService, profiles *ExecutionProfileService, logger *slog.Logger) *QARecheckService {
	if logger == nil {
		logger = slog.Default()
	}
	return &QARecheckService{client: client, projects: projects, profiles: profiles, logger: logger}
}

// QARecheckInput QA 重检的输入参数。选择优先级与任务创建一致：
// segment_group_keys > segment_ids > resource_ids，三者皆空时选择项目内全部资源。
type QARecheckInput struct {
	ProfileID        int
	ResourceIDs      []int
	SegmentIDs       []int
	SegmentGroupKeys []string
}

// QARecheckResult 重检结果汇总，字段镜像 API schema（QaRecheckResult）。
type QARecheckResult struct {
	ProfileID                 int                       `json:"profile_id"`
	ProfileName               string                    `json:"profile_name"`
	ResourcesChecked          int                       `json:"resources_checked"`
	SegmentsChecked           int                       `json:"segments_checked"`
	SegmentsSkippedNoTarget   int                       `json:"segments_skipped_no_target"`
	SegmentsSkippedConcurrent int                       `json:"segments_skipped_concurrent"`
	IssuesNew                 int                       `json:"issues_new"`
	IssuesCleared             int                       `json:"issues_cleared"`
	DispositionsInherited     int                       `json:"dispositions_inherited"`
	Resources                 []QARecheckResourceResult `json:"resources"`
	ResourcesSkippedBusy      []QARecheckBusyResource   `json:"resources_skipped_busy"`
}

// QARecheckResourceResult 单资源的重检结果，字段镜像 API schema（QaRecheckResourceResult）。
type QARecheckResourceResult struct {
	ResourceID                int    `json:"resource_id"`
	ResourceName              string `json:"resource_name"`
	SegmentsChecked           int    `json:"segments_checked"`
	SegmentsSkippedNoTarget   int    `json:"segments_skipped_no_target"`
	SegmentsSkippedConcurrent int    `json:"segments_skipped_concurrent"`
	IssuesNew                 int    `json:"issues_new"`
	IssuesCleared             int    `json:"issues_cleared"`
	DispositionsInherited     int    `json:"dispositions_inherited"`
}

// QARecheckBusyResource 因存在活跃任务而被跳过的资源，字段镜像 API schema（QaRecheckBusyResource）。
type QARecheckBusyResource struct {
	ResourceID  int `json:"resource_id"`
	ActiveJobID int `json:"active_job_id"`
}

// qaRecheckCounters 累计单个资源（或全局）的重检计数。
type qaRecheckCounters struct {
	segmentsChecked       int
	skippedNoTarget       int
	skippedConcurrent     int
	issuesNew             int
	issuesCleared         int
	dispositionsInherited int
}

// Recheck 对选中的段落以指定执行策略当前的 QA 配置与保护规则重跑确定性 QA，
// 按指纹对账继承既有裁决后写回 quality_issues。同步执行、操作幂等可重跑：
// 任一资源失败时中止并返回错误，已处理的资源保持持久化。
func (s *QARecheckService) Recheck(ctx context.Context, actorUserID, projectID int, input QARecheckInput) (*QARecheckResult, error) {
	// 1. 校验项目访问权限（写权限：重检会改写 quality_issues）。
	projectRow, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}

	// 2. 加载执行策略并复核访问权（语义对齐 job.snapshotProfile）。
	tp, err := s.profiles.GetByID(ctx, input.ProfileID)
	if err != nil {
		return nil, err
	}
	if err := s.profiles.CheckAccess(ctx, actorUserID, tp); err != nil {
		return nil, err
	}
	tp.Config.NormalizeContext()
	tp.Config.NormalizePreserveKinds()
	if !tp.Config.QA.Enabled {
		return nil, ErrQAProfileDisabled
	}

	// 3. 解析选择（与任务创建共享同一套解析与权限语义）。
	selection, err := resolveJobSelection(ctx, s.client, projectID, CreateJobInput{
		ResourceIDs:      input.ResourceIDs,
		SegmentIDs:       input.SegmentIDs,
		SegmentGroupKeys: input.SegmentGroupKeys,
	})
	if err != nil {
		return nil, err
	}
	if len(selection) == 0 {
		return nil, ErrResourceNotFound
	}
	totalSegments := 0
	for _, ids := range selection {
		totalSegments += len(ids)
	}
	if totalSegments > maxRecheckSegments {
		return nil, ErrRecheckTooLarge
	}

	// 4. 活跃任务守卫：项目内 pending/running 任务占用的资源跳过重检（非整体失败）。
	busyByResource, err := s.activeJobResources(ctx, projectID)
	if err != nil {
		return nil, err
	}

	// 5. 构建引擎基础配置与项目级术语表。
	baseCfg := qaConfigFromProfile(tp.Config, projectRow)
	projectGlossary, err := buildRecheckGlossary(ctx, s.client, projectRow)
	if err != nil {
		return nil, err
	}
	if projectGlossary != nil {
		baseCfg.Glossary = projectGlossary
	}

	result := &QARecheckResult{
		ProfileID:   tp.ID,
		ProfileName: tp.Name,
		Resources:   make([]QARecheckResourceResult, 0, len(selection)),
	}

	// 6. 逐资源处理（resourceID 升序，保证结果顺序确定）。
	resourceIDs := make([]int, 0, len(selection))
	for rid := range selection {
		if _, busy := busyByResource[rid]; busy {
			continue
		}
		resourceIDs = append(resourceIDs, rid)
	}
	sort.Ints(resourceIDs)

	for rid, jobID := range busyByResource {
		if _, selected := selection[rid]; selected {
			result.ResourcesSkippedBusy = append(result.ResourcesSkippedBusy, QARecheckBusyResource{
				ResourceID:  rid,
				ActiveJobID: jobID,
			})
		}
	}
	sort.Slice(result.ResourcesSkippedBusy, func(i, j int) bool {
		return result.ResourcesSkippedBusy[i].ResourceID < result.ResourcesSkippedBusy[j].ResourceID
	})

	// 一次查询加载全部待处理资源的名称。
	resourcesByID, err := s.loadResources(ctx, resourceIDs)
	if err != nil {
		return nil, err
	}

	// 保护规则重建器：profile 关闭保护时保持 nil（Protected 不重建）。
	// 不组合 ruby protector：它只把 <ruby> 标签剥离进 meta、对 Protected 映射
	// 零贡献（protect/ruby_adapter.go），target 侧 ruby 元素由 QA 屏蔽的
	// ruby/tag 兜底通道覆盖（qa/protect_region.go）。
	var protector protect.Protector
	if tp.Config.Protect.Enabled {
		protector = protect.FromRules(tp.Config.Protect.Rules)
	}

	for _, rid := range resourceIDs {
		resRow, ok := resourcesByID[rid]
		if !ok {
			// 选择解析后资源被并发删除：无段可检，静默跳过。
			continue
		}
		perResource, err := s.recheckResource(ctx, rid, resRow, selection[rid], baseCfg, protector)
		if err != nil {
			return nil, err
		}
		result.Resources = append(result.Resources, QARecheckResourceResult{
			ResourceID:                rid,
			ResourceName:              pathBase(resRow.Path),
			SegmentsChecked:           perResource.segmentsChecked,
			SegmentsSkippedNoTarget:   perResource.skippedNoTarget,
			SegmentsSkippedConcurrent: perResource.skippedConcurrent,
			IssuesNew:                 perResource.issuesNew,
			IssuesCleared:             perResource.issuesCleared,
			DispositionsInherited:     perResource.dispositionsInherited,
		})
		result.ResourcesChecked++
		result.SegmentsChecked += perResource.segmentsChecked
		result.SegmentsSkippedNoTarget += perResource.skippedNoTarget
		result.SegmentsSkippedConcurrent += perResource.skippedConcurrent
		result.IssuesNew += perResource.issuesNew
		result.IssuesCleared += perResource.issuesCleared
		result.DispositionsInherited += perResource.dispositionsInherited
	}
	return result, nil
}

// activeJobResources 查询项目内 pending/running 任务占用的资源，返回 resourceID → jobID 映射。
func (s *QARecheckService) activeJobResources(ctx context.Context, projectID int) (map[int]int, error) {
	jobs, err := s.client.Job.Query().
		Where(
			job.ProjectIDEQ(projectID),
			job.StatusIn(JobStatusPending, JobStatusRunning),
		).
		WithJobResources(func(q *ent.JobResourceQuery) {
			q.WithResource()
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("qa recheck: query active jobs: %w", err)
	}
	busy := make(map[int]int)
	for _, j := range jobs {
		for _, jr := range j.Edges.JobResources {
			res, err := jr.Edges.ResourceOrErr()
			if err != nil || res == nil {
				continue
			}
			if _, ok := busy[res.ID]; !ok {
				busy[res.ID] = j.ID
			}
		}
	}
	return busy, nil
}

// loadResources 一次查询加载资源行，返回 id → 实体映射。
func (s *QARecheckService) loadResources(ctx context.Context, resourceIDs []int) (map[int]*ent.Resource, error) {
	out := make(map[int]*ent.Resource, len(resourceIDs))
	if len(resourceIDs) == 0 {
		return out, nil
	}
	rows, err := s.client.Resource.Query().
		Where(resource.IDIn(resourceIDs...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("qa recheck: query resources: %w", err)
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

// QAConfigFromProfile 将执行策略的 QA 配置映射为引擎 qa.Config（字段逐一对齐，
// 含 Enabled/AutoReject 原值）。job 路径（worker/engine_factory.go 的
// BuildEngineConfig）与重检路径共用本映射，避免两处字段级复制各自漂移——
// ProfileQAConfig 新增字段时只需在此接线一处。
func QAConfigFromProfile(qaCfg schema.ProfileQAConfig, sourceLang, targetLang string) qa.Config {
	return qa.Config{
		Enabled:        qaCfg.Enabled,
		AutoReject:     qaCfg.AutoReject,
		Checks:         qaCfg.Checks,
		LengthMethod:   qa.LengthMethod(qaCfg.LengthMethod),
		LengthRatioMin: qaCfg.LengthRatioMin,
		LengthRatioMax: qaCfg.LengthRatioMax,
		SourceLang:     sourceLang,
		TargetLang:     targetLang,
	}
}

// qaConfigFromProfile 从执行策略配置构建重检的 QA 引擎配置：公共字段映射复用
// QAConfigFromProfile（与 job 路径同源），再按重检语义覆写——Enabled 已由上游
// ErrQAProfileDisabled 门控，强制开启；AutoReject 门控"段落置 rejected"副作用，
// 重检不存在该副作用，清零。
func qaConfigFromProfile(cfg schema.ExecutionProfileConfigData, projectRow *ent.Project) qa.Config {
	out := QAConfigFromProfile(cfg.QA, projectRow.SourceLang, projectRow.TargetLang)
	out.Enabled = true
	out.AutoReject = false
	return out
}

// buildRecheckGlossary 项目启用术语表时加载数据库术语表，加载失败返回错误
// 中止重检（fail-closed）。不能像 quick_translate 那样降级为 nil 继续：术语类
// checker 在 Glossary 为 nil 时静默跳过，重检写回会把既有术语类 issue（含已
// dismissed 裁决）当作已解决清除——quick_translate 的降级只是少报新问题、不删
// 数据，二者后果不同类。
func buildRecheckGlossary(ctx context.Context, client *ent.Client, projectRow *ent.Project) (glossary.Glossary, error) {
	if !projectRow.GlossaryEnabled {
		return nil, nil
	}
	gl, err := NewDatabaseGlossary(ctx, client, projectRow)
	if err != nil {
		return nil, fmt.Errorf("qa recheck: load database glossary: %w", err)
	}
	return gl, nil
}

// recheckResource 对单个资源执行重检：加载选中段、重建保护区、运行 QA 引擎、
// 按批次短事务对账写回。返回该资源的计数。
func (s *QARecheckService) recheckResource(
	ctx context.Context,
	resourceID int,
	resRow *ent.Resource,
	selectedIDs []int,
	baseCfg qa.Config,
	protector protect.Protector,
) (qaRecheckCounters, error) {
	var counters qaRecheckCounters

	// a. 分片加载资源全部选中段：单资源选中段可达重检上限 50000，超出 SQLite
	// 绑定变量上限（32766），必须分片查询后合并；再按 (segment_index, id) 全局
	// 排序——分片查询本身无序，排序保证检测输入与写回结果的顺序确定。
	rows := make([]*ent.Segment, 0, len(selectedIDs))
	for start := 0; start < len(selectedIDs); start += selectionQueryChunkSize {
		end := start + selectionQueryChunkSize
		if end > len(selectedIDs) {
			end = len(selectedIDs)
		}
		chunk, err := s.client.Segment.Query().
			Where(segment.ResourceIDEQ(resourceID), segment.IDIn(selectedIDs[start:end]...)).
			All(ctx)
		if err != nil {
			return counters, fmt.Errorf("qa recheck: query segments of resource %d: %w", resourceID, err)
		}
		rows = append(rows, chunk...)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SegmentIndex != rows[j].SegmentIndex {
			return rows[i].SegmentIndex < rows[j].SegmentIndex
		}
		return rows[i].ID < rows[j].ID
	})

	// b. 构造检测输入：无译文段跳过；meta 解析失败仅记日志；按 profile 重建保护区。
	checkInputs := make([]qa.CheckInput, 0, len(rows))
	targetsByID := make(map[int]*string, len(rows))
	withTarget := make([]*ent.Segment, 0, len(rows))
	for _, row := range rows {
		if row.TargetText == nil {
			counters.skippedNoTarget++
			continue
		}
		var metaMap map[string]any
		if row.Meta != nil {
			if err := json.Unmarshal([]byte(*row.Meta), &metaMap); err != nil {
				s.logger.Warn("qa recheck: parse meta failed", "segmentIndex", row.SegmentIndex, "error", err)
			}
		}
		var protected map[string]string
		if protector != nil {
			_, prot, perr := protect.ProtectText(protector, row.SourceText)
			if perr != nil {
				s.logger.Warn("qa recheck: protect source failed", "segmentID", row.ID, "error", perr)
			} else {
				protected = prot
			}
		}
		checkInputs = append(checkInputs, qa.CheckInput{
			Index:      row.SegmentIndex,
			SourceText: row.SourceText,
			TargetText: *row.TargetText,
			Meta:       metaMap,
			Protected:  protected,
		})
		target := *row.TargetText
		targetsByID[row.ID] = &target
		withTarget = append(withTarget, row)
	}

	// c. 引擎按资源构建（qa.Config 是值类型，clone 一份设置 Format）。
	// 逐段独立的 per-batch checker 喂选中段即可；duplicate（不同原文映射到
	// 相同译文）与文档级 duplicate_source_divergence 是跨段检查，作用域必须
	// 是整个资源（对齐 job 路径的重算语义），否则部分选择时配对段不在输入
	// 中，仍有效的 issue 会在写回时被误清除。引擎在选中段上也会跑 duplicate，
	// 其结果被全资源重算包含（同指纹去重合并），无冲突。
	cfg := baseCfg
	cfg.Format = resRow.Format
	engine := qa.NewEngine(cfg, s.logger)
	fresh := engine.Run(ctx, checkInputs)
	duplicateEnabled := qa.CheckerEnabled(cfg.Checks, qa.CheckDuplicate)
	divergenceEnabled := qa.DuplicateSourceDivergenceEnabled(cfg.Checks)
	if duplicateEnabled || divergenceEnabled {
		crossInputs, err := s.loadCrossSegmentInputs(ctx, resourceID)
		if err != nil {
			return counters, err
		}
		if duplicateEnabled {
			fresh = append(fresh, qa.NewDuplicateTranslationChecker().Check(ctx, crossInputs)...)
		}
		if divergenceEnabled {
			fresh = append(fresh, qa.CheckDuplicateSourceDivergence(crossInputs)...)
		}
	}
	// 合并文档级检查结果后按段分桶，各自去重（指纹不含段索引，必须先分桶）。
	freshByIndex := make(map[int][]qa.QualityIssue)
	for _, issue := range fresh {
		freshByIndex[issue.SegmentIndex] = append(freshByIndex[issue.SegmentIndex], issue)
	}
	for idx, issues := range freshByIndex {
		freshByIndex[idx] = qa.DedupIssues(issues)
	}

	// d. 写回：按批短事务，事务内重读行并做乐观并发校验。
	for start := 0; start < len(withTarget); start += recheckWriteBatchSize {
		end := start + recheckWriteBatchSize
		if end > len(withTarget) {
			end = len(withTarget)
		}
		if err := s.recheckWriteBatch(ctx, withTarget[start:end], targetsByID, freshByIndex, &counters); err != nil {
			return counters, err
		}
	}

	// e. 汇总：有译文且未被并发跳过的段落计入 segments_checked。
	counters.segmentsChecked = len(withTarget) - counters.skippedConcurrent
	return counters, nil
}

// loadCrossSegmentInputs 加载资源全部有译文段，构造跨段检查（duplicate 与
// duplicate_source_divergence）的输入。二者都以前序段为参照定位 issue
// （duplicate 以首个同译文段为参照、divergence 以组内首条译文为规范译法），
// 作用域必须覆盖整个资源（对齐 job 路径 persistDuplicateSourceDivergence 等
// 全量重算的语义）；只喂选中段会把配对段排除在外，导致仍有效的 issue 被
// 写回清除。两个检查都只用 Index/SourceText/TargetText，无需 meta 与保护区；
// 按 (segment_index, id) 排序保证参照段判定与 job 路径一致。
func (s *QARecheckService) loadCrossSegmentInputs(ctx context.Context, resourceID int) ([]qa.CheckInput, error) {
	rows, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID)).
		Order(ent.Asc(segment.FieldSegmentIndex), ent.Asc(segment.FieldID)).
		Select(segment.FieldSegmentIndex, segment.FieldSourceText, segment.FieldTargetText).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("qa recheck: query segments of resource %d for cross-segment checks: %w", resourceID, err)
	}
	inputs := make([]qa.CheckInput, 0, len(rows))
	for _, row := range rows {
		if row.TargetText == nil {
			continue
		}
		inputs = append(inputs, qa.CheckInput{
			Index:      row.SegmentIndex,
			SourceText: row.SourceText,
			TargetText: *row.TargetText,
		})
	}
	return inputs, nil
}

// recheckWriteBatch 在单个短事务内重读一批段落并对账写回 quality_issues。
// 事务内禁止使用 s.client 查询（MaxOpenConns(1) 下会死锁，参照 segment_search_replace.go）。
func (s *QARecheckService) recheckWriteBatch(
	ctx context.Context,
	batch []*ent.Segment,
	targetsByID map[int]*string,
	freshByIndex map[int][]qa.QualityIssue,
	counters *qaRecheckCounters,
) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("qa recheck: begin transaction: %w", err)
	}

	batchIDs := make([]int, 0, len(batch))
	for _, seg := range batch {
		batchIDs = append(batchIDs, seg.ID)
	}
	reread, err := tx.Segment.Query().Where(segment.IDIn(batchIDs...)).All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("qa recheck: reload batch segments: %w", err)
	}
	rereadByID := make(map[int]*ent.Segment, len(reread))
	for _, row := range reread {
		rereadByID[row.ID] = row
	}

	for _, seg := range batch {
		loadedTarget := targetsByID[seg.ID]
		row := rereadByID[seg.ID]
		if row == nil {
			// 段落在重检期间被删除：无法安全写回，按并发跳过处理。
			counters.skippedConcurrent++
			continue
		}
		if row.TargetText == nil || loadedTarget == nil || *row.TargetText != *loadedTarget {
			// 译文已被并发修改：QA 结果基于旧译文，写回会覆盖他人工作，跳过。
			counters.skippedConcurrent++
			continue
		}

		oldIssues := row.QualityIssues
		final := qa.RecheckFinalIssues(freshByIndex[row.SegmentIndex], oldIssues)

		// 单段统计先暂存，确认写回（或指纹未变无需写）后再累计：CAS 未命中时
		// 该段的 QA 结果并未落库，计入摘要会与实际状态矛盾。
		segNew, segCleared, segInherited := 0, 0, 0
		oldFPs := make(map[string]struct{}, len(oldIssues))
		for _, iss := range oldIssues {
			oldFPs[qa.Fingerprint(iss)] = struct{}{}
		}
		finalFPs := make(map[string]struct{}, len(final))
		for _, iss := range final {
			finalFPs[qa.Fingerprint(iss)] = struct{}{}
		}
		for _, iss := range final {
			if _, ok := oldFPs[qa.Fingerprint(iss)]; !ok {
				segNew++
			}
			// 原样保留的语义/守恒类 issue 不计入"继承"——其裁决并非本次对账
			// 所得，只是随写路径保留（口径与 issuesCleared 的排除逻辑对齐）。
			if !iss.IsPending() && !qa.IsSemanticQACode(iss.Code) && !qa.IsConservationCode(iss.Code) {
				segInherited++
			}
		}
		for _, iss := range oldIssues {
			// 语义与守恒类 issue 不由确定性 QA 维护，指纹消失也不计入 cleared。
			if qa.IsSemanticQACode(iss.Code) || qa.IsConservationCode(iss.Code) {
				continue
			}
			if _, ok := finalFPs[qa.Fingerprint(iss)]; !ok {
				segCleared++
			}
		}

		// 指纹集合未变化则跳过写，避免无意义的写放大；状态已确认为最终值，正常计数。
		if qaRecheckIssuesEqual(oldIssues, final) {
			counters.issuesNew += segNew
			counters.issuesCleared += segCleared
			counters.dispositionsInherited += segInherited
			continue
		}

		upd := tx.Segment.UpdateOneID(row.ID).Where(segment.TargetTextEQ(*loadedTarget))
		if len(final) > 0 {
			upd.SetQualityIssues(final)
		} else {
			upd.ClearQualityIssues()
		}
		if _, err := upd.Save(ctx); err != nil {
			if ent.IsNotFound(err) {
				// CAS 未命中：写回前译文又被并发修改，QA 结果未落库，跳过该段
				// 且不计入 issue 统计。
				counters.skippedConcurrent++
				continue
			}
			_ = tx.Rollback()
			return fmt.Errorf("qa recheck: update segment %d: %w", row.ID, err)
		}
		counters.issuesNew += segNew
		counters.issuesCleared += segCleared
		counters.dispositionsInherited += segInherited
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("qa recheck: commit batch: %w", err)
	}
	return nil
}

// qaRecheckIssuesEqual 按 (code, matched_text) 指纹集合比较两批 issue 是否一致
// （思路参照 equalIssues；重检只关心指纹集合，不深度比较全字段）。
func qaRecheckIssuesEqual(a, b []qa.QualityIssue) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	fa := make(map[string]struct{}, len(a))
	for _, iss := range a {
		fa[qa.Fingerprint(iss)] = struct{}{}
	}
	fb := make(map[string]struct{}, len(b))
	for _, iss := range b {
		fb[qa.Fingerprint(iss)] = struct{}{}
	}
	if len(fa) != len(fb) {
		return false
	}
	for fp := range fa {
		if _, ok := fb[fp]; !ok {
			return false
		}
	}
	return true
}
