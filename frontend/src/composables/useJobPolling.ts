import { computed, ref, watch, onMounted, onUnmounted, type Ref } from 'vue'

import { useJobStore } from '@/stores/job'
import { createAdaptivePoller, resolveAdaptiveInterval } from '@/utils/adaptivePolling'

// ── 接口定义 ──

interface UseJobPollingOptions {
  /** 需要轮询的项目 ID */
  projectId: Ref<number | null>
  /** 是否启用列表轮询（如面板是否可见、详情抽屉是否关闭） */
  enabled?: Ref<boolean>
}

interface UseJobPollingReturn {
  /** 是否正在轮询 */
  isPolling: Ref<boolean>
  /** 是否存在活跃（running/pending/paused）任务 */
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
}: UseJobPollingOptions): UseJobPollingReturn {
  const jobStore = useJobStore()

  const isPolling = ref(false)

  // ── 活跃任务检测 ──
  const hasActiveJobs = computed(() =>
    jobStore.jobs.some(
      (j) => j.status === 'running' || j.status === 'pending' || j.status === 'paused',
    ),
  )

  const pollList = (): void => {
    if (!projectId.value || !enabled.value) return
    void jobStore.loadJobs(projectId.value)
  }

  const poller = createAdaptivePoller(
    () => resolveAdaptiveInterval(jobStore.jobs.map((j) => j.status)),
    pollList,
  )

  // ── 统一控制 ──

  const start = (): void => {
    if (isPolling.value) return
    if (!hasActiveJobs.value) return

    isPolling.value = true
    poller.start()
  }

  const stop = (): void => {
    isPolling.value = false
    poller.stop()
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
      poller.start()
    } else {
      poller.stop()
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
