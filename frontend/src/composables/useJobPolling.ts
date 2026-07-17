import { computed, ref, watch, onMounted, onUnmounted, type Ref } from 'vue'

import { useJobStore } from '@/stores/job'

// ── 轮询间隔策略 ──

/** 任务列表轮询间隔（毫秒），按最高活跃状态选择 */
const LIST_POLLING_INTERVALS: Record<string, number | null> = {
  pending: 5_000,
  running: 2_000,
  completed: null,
  failed: null,
  cancelled: null,
}

// ── 接口定义 ──

interface UseJobPollingOptions {
  /** 需要轮询的项目 ID */
  projectId: Ref<number | null>
  /** 是否启用列表轮询（如面板是否可见、详情抽屉是否关闭） */
  enabled?: Ref<boolean>
  /** 任务列表轮询间隔覆盖（毫秒） */
  listInterval?: number
}

interface UseJobPollingReturn {
  /** 是否正在轮询 */
  isPolling: Ref<boolean>
  /** 是否存在活跃（running/pending）任务 */
  hasActiveJobs: Ref<boolean>
  /** 手动启动轮询 */
  start: () => void
  /** 手动停止轮询 */
  stop: () => void
}

// ── Composable ──

export function useJobPolling({
  projectId,
  enabled = ref(true),
  listInterval,
}: UseJobPollingOptions): UseJobPollingReturn {
  const jobStore = useJobStore()

  const isPolling = ref(false)
  let listTimer: ReturnType<typeof setInterval> | null = null

  // ── 活跃任务检测 ──
  const hasActiveJobs = computed(() =>
    jobStore.jobs.some((j) => j.status === 'running' || j.status === 'pending'),
  )

  /**
   * 根据当前任务列表中的最高优先级状态，
   * 计算列表轮询间隔。若无活跃任务则返回 null（停止轮询）。
   */
  const resolveListInterval = (): number | null => {
    if (listInterval != null) return listInterval

    const jobs = jobStore.jobs
    if (jobs.length === 0) return null

    const hasRunning = jobs.some((j) => j.status === 'running')
    if (hasRunning) return LIST_POLLING_INTERVALS.running!

    const hasPending = jobs.some((j) => j.status === 'pending')
    if (hasPending) return LIST_POLLING_INTERVALS.pending!

    return null
  }

  // ── 列表轮询 ──

  const pollList = (): void => {
    if (!projectId.value || !enabled.value) return
    void jobStore.loadJobs(projectId.value)
  }

  const clearListTimer = (): void => {
    if (listTimer) {
      clearInterval(listTimer)
      listTimer = null
    }
  }

  const startListTimer = (): void => {
    if (listTimer || !enabled.value) return
    const interval = resolveListInterval()
    if (interval == null) return

    listTimer = setInterval(() => {
      pollList()
      const newInterval = resolveListInterval()
      if (newInterval == null) {
        clearListTimer()
      }
    }, interval)
  }

  // ── 统一控制 ──

  const start = (): void => {
    if (isPolling.value) return
    if (!hasActiveJobs.value) return

    isPolling.value = true
    startListTimer()
  }

  const stop = (): void => {
    isPolling.value = false
    clearListTimer()
  }

  // ── 页面可见性处理 ──
  const handleVisibility = (): void => {
    if (document.hidden) {
      stop()
    } else if (hasActiveJobs.value) {
      if (enabled.value) pollList()
      start()
    }
  }

  // ── 监听 enabled 变化：仅控制列表轮询 ──
  watch(enabled, (val) => {
    if (val && hasActiveJobs.value) {
      pollList()
      startListTimer()
    } else {
      clearListTimer()
    }
  })

  // ── 监听任务列表变化：有新活跃任务时自动启动轮询 ──
  watch(hasActiveJobs, (active) => {
    if (active && !isPolling.value) {
      if (enabled.value) pollList()
      start()
    } else if (!active && isPolling.value) {
      stop()
    }
  })

  // ── 生命周期 ──
  onMounted(() => {
    document.addEventListener('visibilitychange', handleVisibility)
    if (hasActiveJobs.value) {
      if (enabled.value) pollList()
      start()
    }
  })

  onUnmounted(() => {
    stop()
    document.removeEventListener('visibilitychange', handleVisibility)
  })

  return { isPolling, hasActiveJobs, start, stop }
}
