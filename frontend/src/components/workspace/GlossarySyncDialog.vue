<script setup lang="ts">
import { computed, h, onUnmounted, watch } from 'vue'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NDataTable,
  NModal,
  NProgress,
  NSpin,
  NStatistic,
  NStep,
  NSteps,
  NTag,
  NText,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useGlossaryStore } from '@/stores/glossary'

type SyncStep = 'impact' | 'executing' | 'result' | 'cancelled' | 'error'
type SyncImpactResource = NonNullable<
  ReturnType<typeof useGlossaryStore>['syncImpactData']
>['resources'][number]
type SyncExecuteResourceResult = NonNullable<
  NonNullable<ReturnType<typeof useGlossaryStore>['syncResult']>['resources']
>[number]

const { t } = useI18n()
const glossary = useGlossaryStore()

// ── Props / Emits ──
const props = defineProps<{
  projectId: number | null
}>()

const show = defineModel<boolean>('show', { default: false })

const emit = defineEmits<{
  close: []
  synced: [] // 同步完成，通知父组件刷新数据
}>()

// ── 资源列表表格列定义 ──
const resourceColumns = computed<DataTableColumns<SyncImpactResource>>(() => [
  { type: 'selection' },
  {
    title: t('workspace.glossary.sync.resourcePath'),
    key: 'resource_path',
    ellipsis: { tooltip: true },
  },
  {
    title: t('workspace.glossary.sync.affectedCount'),
    key: 'affected_count',
    width: 120,
    align: 'right',
    render: (row) => h(NText, { type: 'warning' }, { default: () => `${row.affected_count}` }),
  },
])

// ── 结果表格列定义 ──
const resultColumns = computed<DataTableColumns<SyncExecuteResourceResult>>(() => [
  {
    title: t('workspace.glossary.sync.resourcePath'),
    key: 'resource_path',
    ellipsis: { tooltip: true },
  },
  {
    title: t('workspace.glossary.sync.updatedCount'),
    key: 'updated_count',
    width: 100,
    align: 'right',
    render: (row) => h(NText, { type: 'success' }, { default: () => `${row.updated_count}` }),
  },
  {
    title: t('workspace.glossary.sync.skippedCount'),
    key: 'skipped_count',
    width: 100,
    align: 'right',
    render: (row) => h(NText, { depth: 3 }, { default: () => `${row.skipped_count}` }),
  },
])

// ── 计算属性 ──

/** 对话框标题（根据步骤动态切换） */
const dialogTitle = computed(() => {
  const step = glossary.syncStep
  const titles: Record<SyncStep, string> = {
    impact: t('workspace.glossary.sync.titleImpact'),
    executing: t('workspace.glossary.sync.titleExecuting'),
    result: t('workspace.glossary.sync.titleCompleted'),
    cancelled: t('workspace.glossary.sync.titleCancelled'),
    error: t('workspace.glossary.sync.titleError'),
  }
  return titles[step]
})

/** 步骤指示器当前索引 */
const currentStepIndex = computed(() => {
  const map: Record<SyncStep, number> = {
    impact: 1,
    executing: 2,
    result: 3,
    cancelled: 3,
    error: 3,
  }
  return map[glossary.syncStep]
})

/** 步骤指示器状态 */
const currentStepStatus = computed(() => {
  if (glossary.syncStep === 'error') return 'error'
  if (glossary.syncStep === 'cancelled') return 'finish'
  return 'process'
})

// ── 交互方法 ──

const handleResourceSelectionChange = (keys: Array<number | string>): void => {
  glossary.syncSelectedResourceIds = keys as number[]
}

const handleSkip = (): void => {
  glossary.closeSyncDialog()
  emit('close')
}

const handleSubmitSelected = (): void => {
  if (!props.projectId) return
  void glossary.submitSync(props.projectId, 'selected')
}

const handleSubmitAll = (): void => {
  if (!props.projectId) return
  void glossary.submitSync(props.projectId, 'all')
}

const handleCancel = (): void => {
  if (!props.projectId) return
  void glossary.cancelSyncTask(props.projectId)
}

/** 关闭对话框：仅在 result 状态下触发 synced 事件 */
const handleClose = (): void => {
  const currentStep = glossary.syncStep
  glossary.closeSyncDialog()
  emit('close')
  if (currentStep === 'result') {
    emit('synced')
  }
}

const handleRetryImpact = (): void => {
  if (!props.projectId) return
  void glossary.loadSyncImpact(props.projectId)
}

// ── 监听与清理 ──

// 当对话框关闭时清理状态
watch(show, (visible) => {
  if (!visible) {
    glossary.closeSyncDialog()
  }
})

// 组件卸载时停止轮询，防止定时器泄漏
onUnmounted(() => {
  glossary.stopSyncPolling()
})
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="dialogTitle"
    :style="{ width: 'min(640px, 90vw)' }"
    :bordered="false"
    :mask-closable="false"
    :closable="glossary.syncStep !== 'executing'"
  >
    <!-- 步骤指示器 -->
    <NSteps :current="currentStepIndex" :status="currentStepStatus" size="small" class="mb-6">
      <NStep :title="t('workspace.glossary.sync.stepImpact')" />
      <NStep :title="t('workspace.glossary.sync.stepExecute')" />
      <NStep :title="t('workspace.glossary.sync.stepResult')" />
    </NSteps>

    <!-- 步骤 1: 影响分析 -->
    <div v-if="glossary.syncStep === 'impact'">
      <!-- 加载中 -->
      <NSpin v-if="glossary.syncImpactLoading" :show="true" class="py-8">
        <div />
      </NSpin>

      <!-- 错误 -->
      <template v-else-if="glossary.syncImpactError">
        <NAlert type="error" :bordered="false" class="mb-4">
          {{ glossary.syncImpactError }}
        </NAlert>
        <div class="mb-4 flex justify-end">
          <NButton size="small" @click="handleRetryImpact">
            {{ t('workspace.glossary.sync.retry') }}
          </NButton>
        </div>
      </template>

      <!-- 影响分析结果 -->
      <template v-else-if="glossary.syncImpactData">
        <!-- 译文变更提示（含术语源文展示） -->
        <div class="mb-4 rounded-lg border border-lf-border-soft bg-lf-surface-muted/60 p-4">
          <div class="mb-2 text-sm text-lf-text-muted">
            {{
              t('workspace.glossary.sync.targetChangedWithSource', { source: glossary.syncSource })
            }}
          </div>
          <div class="flex items-center gap-3">
            <NTag type="default" :bordered="false">{{ glossary.syncOldTarget }}</NTag>
            <span class="text-lf-text-subtle">→</span>
            <NTag type="success" :bordered="false">{{ glossary.syncNewTarget }}</NTag>
          </div>
        </div>

        <!-- 空影响分析：无需同步 -->
        <template v-if="glossary.syncImpactData.total_affected === 0">
          <NAlert type="info" :bordered="false" class="mb-4">
            {{ t('workspace.glossary.sync.noImpact') }}
          </NAlert>
          <div class="flex justify-end">
            <NButton @click="handleSkip">
              {{ t('workspace.common.close') }}
            </NButton>
          </div>
        </template>

        <!-- 有影响：显示资源列表 -->
        <template v-else>
          <!-- 影响摘要 -->
          <NAlert type="warning" :bordered="false" class="mb-4">
            {{
              t('workspace.glossary.sync.impactSummary', {
                count: glossary.syncImpactData.total_affected,
                resourceCount: glossary.syncImpactData.resources.length,
              })
            }}
          </NAlert>

          <!-- 资源列表 -->
          <NDataTable
            :columns="resourceColumns"
            :data="glossary.syncImpactData.resources"
            :row-key="(row: SyncImpactResource) => row.resource_id"
            :checked-row-keys="glossary.syncSelectedResourceIds"
            :max-height="240"
            size="small"
            class="mb-4"
            @update:checked-row-keys="handleResourceSelectionChange"
          />

          <!-- 操作按钮 -->
          <div class="flex justify-end gap-3">
            <NButton @click="handleSkip">
              {{ t('workspace.glossary.sync.skip') }}
            </NButton>
            <NButton
              :disabled="glossary.syncSelectedResourceCount === 0"
              @click="handleSubmitSelected"
            >
              {{
                t('workspace.glossary.sync.syncSelected', {
                  count: glossary.syncSelectedResourceCount,
                })
              }}
            </NButton>
            <NButton type="primary" @click="handleSubmitAll">
              {{ t('workspace.glossary.sync.syncAll') }}
            </NButton>
          </div>
        </template>
      </template>
    </div>

    <!-- 步骤 2: 执行进度 -->
    <div v-if="glossary.syncStep === 'executing'" class="py-4">
      <!-- 区分等待执行和正在执行状态 -->
      <div class="mb-4 text-center text-lf-text-muted">
        <template v-if="glossary.syncTaskStatus === 'pending'">
          {{ t('workspace.glossary.sync.pending') }}
        </template>
        <template v-else>
          {{ t('workspace.glossary.sync.executingWithSource', { source: glossary.syncSource }) }}
        </template>
      </div>

      <NProgress
        type="line"
        :percentage="glossary.syncProgress"
        :indicator-placement="'inside'"
        :processing="glossary.syncTaskStatus === 'running'"
        class="mb-3"
      />

      <div class="mb-6 text-center text-sm text-lf-text-muted">
        {{
          t('workspace.glossary.sync.progress', {
            processed: glossary.syncProcessed,
            total: glossary.syncTotal,
          })
        }}
      </div>

      <div class="flex justify-center">
        <NButton type="error" ghost @click="handleCancel">
          {{ t('workspace.glossary.sync.cancel') }}
        </NButton>
      </div>
    </div>

    <!-- 步骤 3: 结果摘要 -->
    <div v-if="glossary.syncStep === 'result'" class="py-4">
      <NAlert type="success" :bordered="false" class="mb-4">
        {{ t('workspace.glossary.sync.completed') }}
      </NAlert>

      <div class="mb-4 grid grid-cols-2 gap-4">
        <div class="rounded-lg border border-lf-border-soft bg-lf-surface-muted/60 p-4">
          <NStatistic
            :label="t('workspace.glossary.sync.updated')"
            :value="glossary.syncResult?.total_updated ?? 0"
          />
        </div>
        <div class="rounded-lg border border-lf-border-soft bg-lf-surface-muted/60 p-4">
          <NStatistic
            :label="t('workspace.glossary.sync.skipped')"
            :value="glossary.syncResult?.total_skipped ?? 0"
          />
        </div>
      </div>

      <!-- 按资源分组统计 -->
      <NDataTable
        v-if="glossary.syncResult?.resources?.length"
        :columns="resultColumns"
        :data="glossary.syncResult.resources"
        :row-key="(row: SyncExecuteResourceResult) => row.resource_id"
        :max-height="200"
        size="small"
        class="mb-4"
      />

      <NAlert type="info" :bordered="false" class="mb-4">
        {{ t('workspace.glossary.sync.reviewHint') }}
      </NAlert>

      <div class="flex justify-end">
        <NButton type="primary" @click="handleClose">
          {{ t('workspace.common.confirm') }}
        </NButton>
      </div>
    </div>

    <!-- 取消状态 -->
    <div v-if="glossary.syncStep === 'cancelled'" class="py-4">
      <NAlert type="warning" :bordered="false" class="mb-4">
        {{ t('workspace.glossary.sync.cancelled') }}
      </NAlert>

      <div class="mb-4 grid grid-cols-2 gap-4">
        <div class="rounded-lg border border-lf-border-soft bg-lf-surface-muted/60 p-4">
          <NStatistic
            :label="t('workspace.glossary.sync.processed')"
            :value="glossary.syncProcessed"
          />
        </div>
        <div class="rounded-lg border border-lf-border-soft bg-lf-surface-muted/60 p-4">
          <NStatistic
            :label="t('workspace.glossary.sync.unprocessed')"
            :value="glossary.syncTotal - glossary.syncProcessed"
          />
        </div>
      </div>

      <NAlert type="info" :bordered="false" class="mb-4">
        {{ t('workspace.glossary.sync.cancelledHint') }}
      </NAlert>

      <div class="flex justify-end">
        <NButton type="primary" @click="handleClose">
          {{ t('workspace.common.confirm') }}
        </NButton>
      </div>
    </div>

    <!-- 错误状态 -->
    <div v-if="glossary.syncStep === 'error'" class="py-4">
      <NAlert type="error" :bordered="false" class="mb-4">
        {{ glossary.syncError || t('workspace.glossary.sync.unknownError') }}
      </NAlert>

      <div class="flex justify-end">
        <NButton @click="handleClose">
          {{ t('workspace.common.close') }}
        </NButton>
      </div>
    </div>
  </NModal>
</template>
