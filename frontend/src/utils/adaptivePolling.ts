// ── 自适应轮询定时器 ──

/** 列表/跟踪轮询的状态间隔常量（毫秒） */
export const ACTIVE_POLL_INTERVALS = {
  running: 2_000,
  pending: 5_000,
  paused: 15_000,
} as const

/**
 * 按最高活跃状态解析轮询间隔：running > pending > paused，
 * 无活跃任务返回 null（停止轮询）。paused 慢速轮询保证
 * "在其他会话被恢复"能最终同步回本端。
 */
export function resolveAdaptiveInterval(
  statuses: Iterable<string>,
  intervals: { running: number; pending: number; paused: number } = ACTIVE_POLL_INTERVALS,
): number | null {
  const set = new Set(statuses)
  if (set.has('running')) return intervals.running
  if (set.has('pending')) return intervals.pending
  if (set.has('paused')) return intervals.paused
  return null
}

/**
 * 自适应轮询定时器：每个 tick 重新解析间隔，与当前生效间隔
 * 不一致时以新间隔重建定时器（含变 null 即停止）。间隔重建的
 * 延迟至多一个旧周期，可接受。
 *
 * onTick 自行处理 document.hidden / enabled 等守卫——跳过本次
 * 轮询不影响间隔漂移检测。
 */
export function createAdaptivePoller(
  resolveInterval: () => number | null,
  onTick: () => void,
): { start: () => void; stop: () => void; isRunning: () => boolean } {
  let timer: ReturnType<typeof setInterval> | null = null
  let currentInterval: number | null = null

  const stop = (): void => {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
    currentInterval = null
  }

  const start = (): void => {
    if (timer) return
    const interval = resolveInterval()
    if (interval == null) return

    currentInterval = interval
    timer = setInterval(() => {
      onTick()
      const next = resolveInterval()
      if (next !== currentInterval) {
        stop()
        if (next != null) start()
      }
    }, interval)
  }

  return { start, stop, isRunning: () => timer != null }
}
