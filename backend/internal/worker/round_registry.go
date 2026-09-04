package worker

import (
	"context"
	"fmt"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// roundRegistry 持有每个 (jobResource, roundIndex) 对应的 JobRound 行 ID 与
// 轮次模式。由 PipelineRunner 在任务启动时从 DB 一次性装载，资源 goroutine
// 并发访问（动态建行写回仅在升级兼容的旧任务路径发生）。
//
// 这是「矩阵即事实源」在 worker 侧的投影：行状态转换全部走 service 层
// 条件更新（MarkJobRound*），注册表只做路由——reporter 事件该写到哪一行。
type roundRegistry struct {
	jobID int
	// resourceID → (roundIndex → roundInfo)
	rounds map[int]map[int]*roundInfo
	// mu 保护 rounds 的读写：多个资源 goroutine 并发走动态建行路径
	//（升级兼容：存量任务无预建行、矩阵回填失败不阻断恢复）时，
	// 无锁 map 写会触发 Go runtime fatal（concurrent map writes）。
	mu sync.Mutex
}

type roundInfo struct {
	rowID int
}

// loadJobRounds 从 DB 装载任务全部 JobRound 行，构建注册表。
// 投影只取路由所需三列（id/job_resource_id/round_index），避免读出即弃的
// 启动期读放大（断点恢复走 loadResolved 独立查询）。
// 兼容升级：旧任务无 JobRound 行时返回空注册表（rounds 为空 map），
// 执行链对此的处理是「动态建行」（见 ensureRoundRow）。
func loadJobRounds(ctx context.Context, client *ent.Client, jobID int) (*roundRegistry, error) {
	rows, err := client.JobRound.Query().
		Where(jobround.JobIDEQ(jobID)).
		Select(jobround.FieldID, jobround.FieldJobResourceID, jobround.FieldRoundIndex).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load job rounds: %w", err)
	}
	reg := &roundRegistry{jobID: jobID, rounds: make(map[int]map[int]*roundInfo)}
	for _, row := range rows {
		m, ok := reg.rounds[row.JobResourceID]
		if !ok {
			m = make(map[int]*roundInfo)
			reg.rounds[row.JobResourceID] = m
		}
		m[row.RoundIndex] = &roundInfo{rowID: row.ID}
	}
	return reg, nil
}

// lookup 返回 (jobResourceID, roundIndex) 的行信息；不存在时返回 nil。
func (reg *roundRegistry) lookup(jobResourceID, roundIndex int) *roundInfo {
	if reg == nil {
		return nil
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	m, ok := reg.rounds[jobResourceID]
	if !ok {
		return nil
	}
	return m[roundIndex]
}

// ensureRoundRow 为 (jobResourceID, roundIndex) 返回已有行 ID 或动态建行。
// 升级兼容：旧任务（预建行前的存量）在轮次启动时动态建行，幂等。
func ensureRoundRow(
	ctx context.Context,
	client *ent.Client,
	jobID, jobResourceID, roundIndex int,
	mode string,
) (int, error) {
	existing, err := client.JobRound.Query().
		Where(
			jobround.JobResourceIDEQ(jobResourceID),
			jobround.RoundIndexEQ(roundIndex),
		).
		Only(ctx)
	if err == nil {
		return existing.ID, nil
	}
	if !ent.IsNotFound(err) {
		return 0, fmt.Errorf("query job round: %w", err)
	}
	created, err := client.JobRound.Create().
		SetJobID(jobID).
		SetJobResourceID(jobResourceID).
		SetRoundIndex(roundIndex).
		SetMode(mode).
		SetStatus(service.JobRoundStatusPending).
		Save(ctx)
	if err != nil {
		// 并发建行冲突（唯一索引 job_resource_id+round_index）：重查兜底。
		existing, qerr := client.JobRound.Query().
			Where(
				jobround.JobResourceIDEQ(jobResourceID),
				jobround.RoundIndexEQ(roundIndex),
			).
			Only(ctx)
		if qerr == nil {
			return existing.ID, nil
		}
		return 0, fmt.Errorf("create job round: %w", err)
	}
	return created.ID, nil
}

// ensureLoaded 确保注册表含 (jobResourceID, roundIndex)；动态建行后写回注册表。
// 返回行 ID。资源 goroutine 启动时调用（快照轮数与预建行数一致时零开销）。
// 二次检查：miss 时先无锁走 DB 建行（ensureRoundRow 幂等，唯一索引兜底
// 并发建行冲突），拿到行 ID 后再加锁写回，避免持锁做 I/O。
func (reg *roundRegistry) ensureLoaded(
	ctx context.Context,
	client *ent.Client,
	jobResourceID, roundIndex int,
	mode string,
) (int, error) {
	if info := reg.lookup(jobResourceID, roundIndex); info != nil {
		return info.rowID, nil
	}
	rowID, err := ensureRoundRow(ctx, client, reg.jobID, jobResourceID, roundIndex, mode)
	if err != nil {
		return 0, err
	}
	reg.mu.Lock()
	m, ok := reg.rounds[jobResourceID]
	if !ok {
		m = make(map[int]*roundInfo)
		reg.rounds[jobResourceID] = m
	}
	m[roundIndex] = &roundInfo{rowID: rowID}
	reg.mu.Unlock()
	return rowID, nil
}

// loadResolved 从 DB 恢复某资源各非翻译轮已解决段集合（按 mode 分组，DB Segment ID）。
// 恢复语义：内存 resolvedByMode 是跨同模式轮累积集合，持久化后恢复 =
// 各同模式轮 job_round_segments 关联行的并集（逐段实时追加，崩溃/失败后
// 已落盘段不重扫）。两步查询：先取该资源**非翻译**轮的 (id, mode)，
// 再按行 ID 集合查 join 行；无此类轮次行时跳过第二次查询。
//
// 排除 translate 轮是必须的读放大控制：translate 轮同样登记断点行（其
// segment_completed 由集合基数派生），但跨轮增量由 Segment.status 驱动、
// resolvedByMode 里根本没有 translate 键，把整个翻译断点集读出来只会被丢弃。
func loadResolved(ctx context.Context, client *ent.Client, jobResourceID int) (map[string]map[int]struct{}, error) {
	rounds, err := client.JobRound.Query().
		Where(
			jobround.JobResourceIDEQ(jobResourceID),
			jobround.ModeNEQ(pipeline.RoundModeTranslate),
		).
		Select(jobround.FieldID, jobround.FieldMode).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load resolved rounds: %w", err)
	}
	out := make(map[string]map[int]struct{})
	if len(rounds) == 0 {
		return out, nil
	}
	roundIDs := make([]int, 0, len(rounds))
	modeByRound := make(map[int]string, len(rounds))
	for _, row := range rounds {
		roundIDs = append(roundIDs, row.ID)
		modeByRound[row.ID] = row.Mode
	}
	links, err := client.JobRoundSegment.Query().
		Where(jobroundsegment.JobRoundIDIn(roundIDs...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load resolved segments: %w", err)
	}
	for _, link := range links {
		mode := modeByRound[link.JobRoundID]
		set, ok := out[mode]
		if !ok {
			set = make(map[int]struct{})
			out[mode] = set
		}
		set[link.SegmentID] = struct{}{}
	}
	return out, nil
}
