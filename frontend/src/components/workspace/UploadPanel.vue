<script setup lang="ts">
import { computed, ref, watch, onMounted, onUnmounted } from 'vue'
import { NButton, NIcon, NProgress, NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import type { UploadTask } from '@/stores/projectWorkspace'

// ── Props & Emits ──

defineProps<{
  projectId: number
}>()

defineEmits<{
  refresh: []
}>()

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

// ── 面板展开/收缩状态 ──

const isExpanded = ref(false)

// ── 已调度自动消失的任务 ID 集合 ──

const autoDismissScheduled = ref(new Set<string>())

// ── 汇总统计 ──

const activeTasks = computed(() =>
  workspace.uploadTasks.filter((t) => ['prechecking', 'uploading', 'processing'].includes(t.stage)),
)

const completedTasks = computed(() => workspace.uploadTasks.filter((t) => t.stage === 'complete'))

const errorTasks = computed(() => workspace.uploadTasks.filter((t) => t.stage === 'error'))

const partialTasks = computed(() => workspace.uploadTasks.filter((t) => t.stage === 'partial'))

/** 主导阶段：取优先级最高的任务阶段 */
const stagePriority: Record<UploadTask['stage'], number> = {
  error: 0,
  partial: 1,
  prechecking: 2,
  uploading: 3,
  processing: 4,
  complete: 5,
}

const dominantStage = computed<UploadTask['stage'] | 'idle'>(() => {
  const tasks = workspace.uploadTasks
  if (tasks.length === 0) return 'idle'
  return tasks.reduce((best, t) => (stagePriority[t.stage] < stagePriority[best.stage] ? t : best))
    .stage
})

/** 平均进度（仅 uploading 阶段的任务） */
const averageProgress = computed(() => {
  const uploadingTasks = workspace.uploadTasks.filter((t) => t.stage === 'uploading')
  if (uploadingTasks.length === 0) return 0
  const sum = uploadingTasks.reduce((acc, t) => acc + t.progress, 0)
  return Math.round(sum / uploadingTasks.length)
})

/** 摘要文案 */
const summaryText = computed(() => {
  const count = workspace.uploadTasks.length
  const stage = dominantStage.value
  const result = workspace.lastUploadResult

  if (stage === 'prechecking') {
    return t('workspace.uploadPanel.collapsedPrechecking', { count })
  }
  if (stage === 'uploading') {
    return t('workspace.uploadPanel.collapsedUploading', {
      count: activeTasks.value.length,
      percent: averageProgress.value,
    })
  }
  if (stage === 'processing') {
    return t('workspace.uploadPanel.collapsedProcessing')
  }
  if (stage === 'complete') {
    const total = result?.summary.total ?? completedTasks.value.length
    return t('workspace.uploadPanel.collapsedComplete', { count: total })
  }
  if (stage === 'partial') {
    const total = result?.summary.total ?? partialTasks.value.length
    return t('workspace.uploadPanel.collapsedPartial', { count: total })
  }
  if (stage === 'error') {
    const failed = result?.summary.failed ?? errorTasks.value.length
    return t('workspace.uploadPanel.collapsedError', { count: failed })
  }
  return ''
})

/** 展开态标题 */
const expandedTitle = computed(() => {
  const stage = dominantStage.value
  const activeCount = activeTasks.value.length

  if (stage === 'prechecking') {
    return t('workspace.uploadPanel.expandedPrechecking', { count: activeCount })
  }
  if (stage === 'uploading') {
    return t('workspace.uploadPanel.expandedUploading', { count: activeCount })
  }
  if (stage === 'processing') {
    return t('workspace.uploadPanel.expandedProcessing')
  }
  if (stage === 'complete') {
    return t('workspace.uploadPanel.expandedComplete')
  }
  if (stage === 'partial') {
    return t('workspace.uploadPanel.expandedPartial')
  }
  if (stage === 'error') {
    return t('workspace.uploadPanel.expandedError')
  }
  return t('workspace.uploadPanel.title')
})

// ── 展开/收缩自动逻辑 ──

watch(
  () => workspace.uploadTasks,
  (tasks) => {
    // 有活跃任务时自动展开
    const hasActive = tasks.some((t) =>
      ['prechecking', 'uploading', 'processing'].includes(t.stage),
    )
    if (hasActive) {
      isExpanded.value = true
      return
    }

    // 所有任务完成时自动收缩（有结果数据时保持展开）
    const allComplete = tasks.length > 0 && tasks.every((t) => t.stage === 'complete')
    if (allComplete && !workspace.lastUploadResult) {
      isExpanded.value = false
      return
    }

    // 有 partial 或 error 时保持展开
    const hasProblematic = tasks.some((t) => t.stage === 'partial' || t.stage === 'error')
    if (hasProblematic) {
      isExpanded.value = true
    }
  },
  { deep: true },
)

// ── 自动消失逻辑（仅 complete 状态） ──

watch(
  () => workspace.uploadTasks,
  (tasks) => {
    for (const task of tasks) {
      if (task.stage === 'complete' && !autoDismissScheduled.value.has(task.id)) {
        autoDismissScheduled.value.add(task.id)

        // 3 秒后自动收缩（如果所有任务都是 complete）
        setTimeout(() => {
          const allComplete = workspace.uploadTasks.every((t) => t.stage === 'complete')
          if (allComplete) {
            isExpanded.value = false
          }
        }, 3000)

        // 5 秒后自动移除任务
        setTimeout(() => {
          workspace.removeUploadTask(task.id)
          autoDismissScheduled.value.delete(task.id)
        }, 5000)
      }
    }
  },
  { deep: true },
)

// ── Escape 快捷键收缩 ──

const handleKeydown = (e: KeyboardEvent) => {
  if (e.key === 'Escape' && isExpanded.value) {
    e.stopPropagation()
    isExpanded.value = false
  }
}

onMounted(() => {
  document.addEventListener('keydown', handleKeydown)
})

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydown)
})

// ── 工具方法 ──

const toggleExpand = () => {
  isExpanded.value = !isExpanded.value
}

const handleClearAll = () => {
  if (dominantStage.value === 'complete' || dominantStage.value === 'idle') {
    workspace.clearCompletedUploadTasks()
  } else {
    workspace.clearAllUploadTasks()
  }
  workspace.lastUploadResult = null
  autoDismissScheduled.value.clear()
}

/** 是否显示清除按钮 */
const showClearButton = computed(() => {
  const stage = dominantStage.value
  return (
    stage === 'complete' ||
    stage === 'partial' ||
    stage === 'error' ||
    stage === 'idle' ||
    hasResult.value
  )
})

/** 是否显示迷你进度条（仅收缩态 uploading） */
const showMiniProgress = computed(() => {
  return dominantStage.value === 'uploading' && !isExpanded.value
})

// ── 上传结果展示 ──

const lastUploadResult = computed(() => workspace.lastUploadResult)
const hasResult = computed(() => lastUploadResult.value !== null)
const hasActiveTasks = computed(() => activeTasks.value.length > 0)

type ResultRow = {
  rowType: 'result' | 'incremental' | 'replace' | 'skipped'
  path: string
  action: string
  error?: string
}

const summaryItems = computed(() => {
  const result = lastUploadResult.value
  if (!result) return []
  return [
    {
      key: 'created',
      label: t('workspace.uploadResult.summary.created'),
      value: result.summary.created,
      tier: 'primary',
      dotColor: 'bg-emerald-500',
    },
    {
      key: 'incrementallyUpdated',
      label: t('workspace.uploadResult.summary.incrementallyUpdated'),
      value: result.summary.incrementallyUpdated,
      tier: 'secondary',
      dotColor: 'bg-blue-400',
    },
    {
      key: 'replaced',
      label: t('workspace.uploadResult.summary.replaced'),
      value: result.summary.replaced,
      tier: 'secondary',
      dotColor: 'bg-violet-400',
    },
    {
      key: 'skipped',
      label: t('workspace.uploadResult.summary.skipped'),
      value: result.summary.skipped,
      tier: 'secondary',
      dotColor: 'bg-slate-400 dark:bg-slate-500',
    },
    {
      key: 'failed',
      label: t('workspace.uploadResult.summary.failed'),
      value: result.summary.failed,
      tier: 'primary',
      dotColor: 'bg-red-500',
    },
  ]
})

const resultRows = computed<ResultRow[]>(() => {
  const result = lastUploadResult.value
  if (!result) return []

  const rows: ResultRow[] = []

  // API 直接返回的结果
  for (const item of result.response.items) {
    rows.push({
      rowType: 'result',
      path: item.path,
      action: item.action,
      error: item.error,
    })
  }

  // 增量更新结果
  for (const item of result.incrementalResults) {
    rows.push({
      rowType: 'incremental',
      path: item.item.path,
      action: item.error ? 'failed' : 'incremental_updated',
      error: item.error,
    })
  }

  // 覆盖替换结果
  for (const item of result.replaceResults) {
    rows.push({
      rowType: 'replace',
      path: item.item.path,
      action: item.error ? 'failed' : 'replaced',
      error: item.error,
    })
  }

  // 跳过的项
  for (const item of result.skippedItems) {
    rows.push({
      rowType: 'skipped',
      path: item.path,
      action: 'skipped',
    })
  }

  return rows
})

const getActionLabel = (action: string): string => {
  switch (action) {
    case 'created':
      return t('workspace.uploadResult.summary.created')
    case 'incremental_updated':
      return t('workspace.uploadResult.summary.incrementallyUpdated')
    case 'replaced':
      return t('workspace.uploadResult.summary.replaced')
    case 'skipped':
      return t('workspace.uploadResult.summary.skipped')
    case 'failed':
      return t('workspace.uploadResult.summary.failed')
    case 'conflict':
      return t('workspace.uploadResult.summary.conflicts')
    default:
      return action
  }
}

const getActionTagType = (action: string): 'success' | 'warning' | 'error' | 'default' => {
  if (action === 'created' || action === 'incremental_updated' || action === 'replaced') {
    return 'success'
  }
  if (action === 'conflict' || action === 'skipped') {
    return 'warning'
  }
  if (action === 'failed') {
    return 'error'
  }
  return 'default'
}

const getRowAccentClass = (action: string): string => {
  if (action === 'failed') return 'border-l-red-300 dark:border-l-red-500/60'
  if (action === 'conflict') return 'border-l-amber-300 dark:border-l-amber-500/60'
  return 'border-l-transparent'
}

const getActionDetail = (row: ResultRow): string => {
  if (row.action === 'created') return t('workspace.uploadResult.details.created')
  if (row.action === 'incremental_updated')
    return t('workspace.uploadResult.details.incrementalUpdated')
  if (row.action === 'replaced') return t('workspace.uploadResult.details.replaced')
  if (row.action === 'failed') return row.error || t('workspace.uploadResult.details.failed')
  if (row.action === 'skipped') return t('workspace.uploadResult.details.skipped')
  if (row.action === 'conflict')
    return t('workspace.uploadResult.details.conflict', { name: row.path })
  return row.action
}

/** 阶段对应的图标背景色 */
const stageIconBgClass = (stage: UploadTask['stage']): string => {
  const map: Record<UploadTask['stage'], string> = {
    prechecking: 'bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300',
    uploading: 'bg-blue-50 text-blue-600 dark:bg-blue-500/15 dark:text-blue-300',
    processing: 'bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-300',
    complete: 'bg-emerald-50 text-emerald-600 dark:bg-emerald-500/15 dark:text-emerald-300',
    partial: 'bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-300',
    error: 'bg-red-50 text-red-600 dark:bg-red-500/15 dark:text-red-300',
  }
  return map[stage]
}
</script>

<template>
  <Transition name="panel">
    <div
      v-show="workspace.uploadTasks.length > 0 || hasResult"
      class="fixed inset-x-0 bottom-0 z-40"
    >
      <div
        class="mx-auto w-full max-w-4xl overflow-hidden rounded-t-2xl border-t border-lf-border-soft bg-lf-surface/95 shadow-[0_-2px_16px_rgba(0,0,0,0.08)] backdrop-blur-xl"
      >
        <!-- 收缩态头部 -->
        <button
          type="button"
          class="flex h-14 w-full cursor-pointer items-center gap-3 px-4 text-left transition-colors hover:bg-lf-surface-muted/50 sm:px-6"
          @click="toggleExpand"
        >
          <!-- 状态图标 -->
          <div
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg"
            :class="stageIconBgClass(dominantStage === 'idle' ? 'complete' : dominantStage)"
          >
            <IconCarbonAsync
              v-if="dominantStage === 'prechecking' || dominantStage === 'processing'"
              class="h-4 w-4 animate-spin"
            />
            <IconCarbonUpload v-else-if="dominantStage === 'uploading'" class="h-4 w-4" />
            <IconCarbonCheckmark v-else-if="dominantStage === 'complete'" class="h-4 w-4" />
            <IconCarbonWarningAlt
              v-else-if="dominantStage === 'partial' || dominantStage === 'error'"
              class="h-4 w-4"
            />
            <IconCarbonCheckmark v-else class="h-4 w-4" />
          </div>

          <!-- 摘要文案 / 展开态标题 -->
          <span class="min-w-0 flex-1 truncate text-sm font-medium text-lf-text-strong">
            {{ isExpanded ? expandedTitle : summaryText }}
          </span>

          <!-- 迷你进度条（收缩态 uploading） -->
          <div v-if="showMiniProgress" class="hidden w-24 sm:block">
            <NProgress
              type="line"
              :percentage="averageProgress"
              :show-indicator="false"
              :stroke-width="6"
              status="info"
              class="upload-progress-bar"
            />
          </div>

          <!-- 展开/收缩按钮 -->
          <NButton quaternary size="tiny" class="shrink-0" @click.stop="toggleExpand">
            <template #icon>
              <NIcon>
                <IconCarbonChevronUp v-if="!isExpanded" />
                <IconCarbonChevronDown v-else />
              </NIcon>
            </template>
          </NButton>

          <!-- 关闭/清除按钮 -->
          <NButton
            v-if="showClearButton"
            quaternary
            size="tiny"
            class="shrink-0"
            @click.stop="handleClearAll"
          >
            <template #icon>
              <NIcon><IconCarbonClose /></NIcon>
            </template>
          </NButton>
        </button>

        <!-- 展开态内容 -->
        <Transition name="expand">
          <div v-if="isExpanded" class="border-t border-lf-border-soft">
            <!-- 结果详情区（仅 complete/partial/error 时显示） -->
            <template v-if="hasResult && !hasActiveTasks">
              <!-- 摘要指标区 -->
              <div
                class="grid grid-cols-3 gap-2 border-t border-lf-border-soft px-4 py-3 lg:grid-cols-5"
              >
                <div
                  v-for="item in summaryItems"
                  :key="item.key"
                  class="flex items-center gap-2"
                  :class="{ 'opacity-40': item.value === 0 }"
                >
                  <span class="h-2 w-2 shrink-0 rounded-full" :class="item.dotColor" />
                  <span class="text-sm font-semibold text-lf-text-strong">{{ item.value }}</span>
                  <span class="text-xs text-lf-text-muted">{{ item.label }}</span>
                </div>
              </div>

              <!-- 文件结果列表 -->
              <div class="max-h-[40vh] space-y-2 overflow-y-auto px-4 py-3">
                <div
                  v-for="row in resultRows"
                  :key="`${row.rowType}:${row.path}:${row.action}`"
                  class="flex flex-col gap-1.5 rounded-lg border border-l-3 border-lf-border/60 bg-lf-surface px-3 py-2.5"
                  :class="getRowAccentClass(row.action)"
                >
                  <div class="flex items-center gap-2">
                    <span class="min-w-0 truncate text-sm font-medium text-lf-text-strong">
                      {{ row.path }}
                    </span>
                    <NTag :type="getActionTagType(row.action)" size="small" :bordered="false">
                      {{ getActionLabel(row.action) }}
                    </NTag>
                  </div>
                  <p v-if="row.error" class="text-xs text-red-500 dark:text-red-400">
                    {{ row.error }}
                  </p>
                  <p v-else class="text-xs leading-5 text-lf-text-muted">
                    {{ getActionDetail(row) }}
                  </p>
                </div>
              </div>
            </template>

            <!-- 底部操作栏 -->
            <div
              v-if="showClearButton"
              class="flex items-center justify-end border-t border-lf-border-soft px-4 py-2 sm:px-6"
            >
              <NButton size="small" quaternary @click="handleClearAll">
                {{ t('workspace.uploadPanel.clearAll') }}
              </NButton>
            </div>
          </div>
        </Transition>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
/* 面板整体出现/消失 */
.panel-enter-active {
  transition:
    transform 0.3s ease-out,
    opacity 0.3s ease-out;
}
.panel-leave-active {
  transition:
    transform 0.2s ease-in,
    opacity 0.2s ease-in;
}
.panel-enter-from {
  transform: translateY(100%);
  opacity: 0;
}
.panel-leave-to {
  transform: translateY(100%);
  opacity: 0;
}

/* 展开/收缩过渡 */
.expand-enter-active {
  transition:
    max-height 0.25s ease-out,
    opacity 0.25s ease-out;
  max-height: 60vh;
  overflow: hidden;
}
.expand-leave-active {
  transition:
    max-height 0.2s ease-in,
    opacity 0.2s ease-in;
  overflow: hidden;
}
.expand-enter-from {
  max-height: 56px;
  opacity: 0;
}
.expand-leave-to {
  max-height: 56px;
  opacity: 0;
}

/* 进度条自定义样式 */
.upload-progress-bar :deep(.n-progress-graph-line-fill) {
  background: linear-gradient(90deg, #3b82f6, #6366f1);
  border-radius: 3px;
  transition: width 0.3s ease-out;
}

.upload-progress-bar :deep(.n-progress-graph-line-rail) {
  background: var(--lf-border-soft);
  border-radius: 3px;
}
</style>
