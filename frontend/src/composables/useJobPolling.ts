import { ref, watch, onMounted, onUnmounted, type Ref } from 'vue'

import { useTranslationJobStore } from '@/stores/translationJob'

// ── 轮询间隔策略 ──

/** 任务列表轮询间隔（毫秒），按最高活跃状态选择 */
const LIST_POLLING_INTERVALS: Record<string, number | null> = {
  pending: 5_000,
  running: 2_000,
  completed: null,
  failed: null,
  cancelled: null,
}

/** 事件轮询固定间隔 */
const EVENTS_POLLING_INTERVAL = 10_000

/** 默认任务列表轮询兜底间隔 */
const DEFAULT_LIST_INTERVAL = 3_000

// ── 接口定义 ──

interface UseJobPollingOptions {
  /** 需要轮询的项目 ID */
  projectId: Ref<number | null>
  /** 是否启用轮询（如面板是否可见） */
  enabled?: Ref<boolean>
  /** 任务列表轮询间隔覆盖（毫秒） */
  listInterval?: number
  /** 事件轮询间隔覆盖（毫秒） */
  eventsInterval?: number
}

interface UseJobPollingReturn {
  /** 是否正在轮询 */
  isPolling: Ref<boolean>
  /** 手动启动轮询 */
  start: () => void
  /** 手动停止轮询 */
  stop: () => void
}

// ── 终态集合 ──
const TERMINAL_STATUSES = new Set(['completed', 'failed', 'cancelled'])

// ── Composable ──

export function useJobPolling({
  projectId,
  enabled = ref(true),
  listInterval,
  eventsInterval = EVENTS_POLLING_INTERVAL,
}: UseJobPollingOptions): UseJobPollingReturn {
  const jobStore = useTranslationJobStore()

  const isPolling = ref(false)
  let listTimer: ReturnType<typeof setInterval> | null = null
  let eventsTimer: ReturnType<typeof setInterval> | null = null

  /**
   * 根据当前任务列表中的最高优先级状态，
   * 计算列表轮询间隔。若所有任务均处于终态则返回 null（停止轮询）。
   */
  const resolveListInterval = (): number | null => {
    // 如果有覆盖值，直接使用
    if (listInterval != null) return listInterval

    const jobs = jobStore.jobs
    if (jobs.length === 0) return DEFAULT_LIST_INTERVAL

    // 取最高优先级状态：running > pending > 其他
    const hasRunning = jobs.some((j) => j.status === 'running')
    if (hasRunning) return LIST_POLLING_INTERVALS.running!

    const hasPending = jobs.some((j) => j.status === 'pending')
    if (hasPending) return LIST_POLLING_INTERVALS.pending!

    // 所有任务均处于终态
    return null
  }

  /** 轮询任务列表 */
  const pollList = (): void => {
    if (!projectId.value || !enabled.value) return
    void jobStore.loadJobs(projectId.value)
  }

  /** 轮询选中任务的事件 */
  const pollEvents = (): void => {
    const selected = jobStore.selectedJob
    if (!selected || TERMINAL_STATUSES.has(selected.status)) return
    void jobStore.loadEvents(selected.id)
  }

  /** 清除所有定时器 */
  const clearTimers = (): void => {
    if (listTimer) {
      clearInterval(listTimer)
      listTimer = null
    }
    if (eventsTimer) {
      clearInterval(eventsTimer)
      eventsTimer = null
    }
  }

  /** 启动列表轮询定时器 */
  const startListTimer = (): void => {
    const interval = resolveListInterval()
    if (interval == null) return // 所有任务终态，不启动

    listTimer = setInterval(() => {
      pollList()
      // 每次轮询后重新评估间隔（状态可能已变化）
      const newInterval = resolveListInterval()
      if (newInterval == null) {
        // 任务全部终态，停止列表轮询
        if (listTimer) {
          clearInterval(listTimer)
          listTimer = null
        }
      }
    }, interval)
  }

  /** 启动事件轮询定时器 */
  const startEventsTimer = (): void => {
    eventsTimer = setInterval(() => {
      pollEvents()
    }, eventsInterval)
  }

  const start = (): void => {
    if (isPolling.value) return
    isPolling.value = true

    startListTimer()
    startEventsTimer()
  }

  const stop = (): void => {
    isPolling.value = false
    clearTimers()
  }

  // ── 页面可见性处理 ──
  const handleVisibility = (): void => {
    if (document.hidden) {
      stop()
    } else if (enabled.value) {
      // 恢复可见时立即拉取一次最新数据
      pollList()
      pollEvents()
      // 重新启动轮询
      start()
    }
  }

  // ── 监听 enabled 变化 ──
  watch(enabled, (val) => {
    if (val) {
      pollList()
      pollEvents()
      start()
    } else {
      stop()
    }
  })

  // ── 监听 selectedJob 状态变化，终态时停止事件轮询 ──
  watch(
    () => jobStore.selectedJob?.status,
    (status) => {
      if (!status || TERMINAL_STATUSES.has(status)) {
        // 任务终态：拉取最后一次事件，然后停止事件轮询
        if (eventsTimer) {
          pollEvents()
          clearInterval(eventsTimer)
          eventsTimer = null
        }
      } else if (isPolling.value && !eventsTimer) {
        // 从终态变为非终态（如 retry 后），重新启动事件轮询
        startEventsTimer()
      }
    },
  )

  // ── 生命周期 ──
  onMounted(() => {
    document.addEventListener('visibilitychange', handleVisibility)
    if (enabled.value) {
      pollList()
      pollEvents()
      start()
    }
  })

  onUnmounted(() => {
    stop()
    document.removeEventListener('visibilitychange', handleVisibility)
  })

  return { isPolling, start, stop }
}
