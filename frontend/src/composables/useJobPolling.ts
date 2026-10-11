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
  /** 是否存在活跃（running/pausing/pending/paused）任务 */
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
  let mounted = false

  // ── 活跃任务检测 ──
  const pollingInterval = computed(() =>
    resolveAdaptiveInterval(jobStore.jobs.map((job) => job.status)),
  )
  const hasActiveJobs = computed(() => pollingInterval.value != null)

  const pollList = (): void => {
    if (!projectId.value || !enabled.value || document.hidden) return
    void jobStore.loadJobs(projectId.value)
  }

  const poller = createAdaptivePoller(() => pollingInterval.value, pollList)

  // ── 统一控制 ──

  const start = (): void => {
    if (isPolling.value) return
    if (!mounted || !projectId.value || !enabled.value || document.hidden || !hasActiveJobs.value)
      return

    poller.start()
    isPolling.value = poller.isRunning()
  }

  const stop = (): void => {
    isPolling.value = false
    poller.stop()
  }

  // Rebuild immediately when the service moves paused work into running or pausing.
  const reconcile = (): void => {
    stop()
    start()
    if (isPolling.value) pollList()
  }

  watch([enabled, projectId, pollingInterval], reconcile)

  // ── 生命周期 ──
  onMounted(() => {
    mounted = true
    document.addEventListener('visibilitychange', reconcile)
    reconcile()
  })

  onUnmounted(() => {
    mounted = false
    stop()
    document.removeEventListener('visibilitychange', reconcile)
  })

  return { isPolling, hasActiveJobs, start, stop }
}
