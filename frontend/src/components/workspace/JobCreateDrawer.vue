<script setup lang="ts">
import type { FormInst, FormRules } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NRadio,
  NRadioGroup,
  NSelect,
  NSpace,
  NSwitch,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'

import type { JobTargetMode } from '@/composables/useJobActions'

type ExecutionPlanTemplate = ApiSchemas['ExecutionPlanTemplate']
type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type CreateJobRequest = ApiSchemas['CreateJobRequest']
type SegmentFilter = NonNullable<CreateJobRequest['segment_filter']>

const { t } = useI18n()
const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()

const show = defineModel<boolean>('show', { default: false })

const props = defineProps<{
  formRef: FormInst | null
  targetMode: JobTargetMode
  targetResourceIds: number[]
  targetSegmentIds: number[]
  targetGroupKeys: string[]
  executionPlanId: number | null
  autoApprove: boolean
  segmentFilter: SegmentFilter | undefined
  formRules: FormRules
  executionPlanOptions: Array<{ label: string; value: number }>
  submitting: boolean
  segmentCount: number
  selectedPlanTemplate: ExecutionPlanTemplate | null
}>()

const emit = defineEmits<{
  'update:executionPlanId': [value: number | null]
  'update:autoApprove': [value: boolean]
  'update:segmentFilter': [value: SegmentFilter | undefined]
  submit: []
  close: []
}>()

const segmentFilterOptions = [
  { value: 'pending_only', labelKey: 'workspace.job.segmentFilter.pendingOnly' },
  { value: 'skip_approved', labelKey: 'workspace.job.segmentFilter.skipApproved' },
  { value: 'all', labelKey: 'workspace.job.segmentFilter.all' },
]

const overrideSegmentFilter = computed(() => props.segmentFilter !== undefined)

const handleToggleOverride = (enabled: boolean): void => {
  emit('update:segmentFilter', enabled ? 'pending_only' : undefined)
}

const formatRoundSummary = (round: ExecutionRoundConfig, index: number): string => {
  const modeConfig =
    round.mode === 'extract'
      ? round.extract
      : round.mode === 'adjudicate'
        ? round.adjudicate
        : round.translate
  const batchSize = modeConfig?.batch_size ?? 0
  const maxWordsPerBatch = modeConfig?.max_words_per_batch ?? 0
  const parts: string[] = []
  if (batchSize > 0) {
    parts.push(t('workspace.job.planPreviewBatchSegments', { batchSize }))
  }
  if (maxWordsPerBatch > 0) {
    parts.push(t('workspace.job.planPreviewBatchWords', { maxWordsPerBatch }))
  }
  const batchInfo = parts.length === 0 ? t('workspace.job.planPreviewNoBatch') : parts.join(' / ')
  const modeKey =
    round.mode === 'extract'
      ? 'workspace.job.planPreviewModeExtract'
      : round.mode === 'adjudicate'
        ? 'workspace.job.planPreviewModeAdjudicate'
        : 'workspace.job.planPreviewModeTranslate'
  return t('workspace.job.planPreviewRoundItem', {
    index: index + 1,
    mode: t(modeKey),
    batch: batchInfo,
    concurrency: round.concurrency,
  })
}
</script>

<template>
  <NDrawer v-model:show="show" :width="480" placement="right">
    <NDrawerContent :title="t('workspace.job.createTitle')" closable>
      <!-- 翻译内容摘要 -->
      <NAlert type="info" :bordered="false" class="mb-4">
        <template #header>
          {{ t('workspace.job.contentSummaryTitle') }}
        </template>
        <div class="space-y-1 text-sm">
          <div v-if="targetMode === 'resources' && targetGroupKeys.length > 0">
            {{ t('workspace.job.contentSummaryChapters', { count: targetGroupKeys.length }) }}
          </div>
          <div v-else-if="targetMode === 'resources'">
            {{ t('workspace.job.contentSummaryResources', { count: targetResourceIds.length }) }}
          </div>
          <div v-else>
            {{ t('workspace.job.targetSegments', { count: targetSegmentIds.length }) }}
          </div>
          <div v-if="segmentCount > 0">
            {{ t('workspace.job.contentSummarySegments', { count: segmentCount }) }}
          </div>
        </div>
      </NAlert>

      <NForm
        ref="formRef"
        :model="{ execution_plan_id: executionPlanId, auto_approve: autoApprove }"
        :rules="formRules"
        label-placement="top"
      >
        <NFormItem :label="t('workspace.job.form.executionPlan')" path="execution_plan_id">
          <NSelect
            :value="executionPlanId"
            :options="executionPlanOptions"
            :loading="executionPlanTemplatesStore.loading"
            :placeholder="t('workspace.job.form.executionPlanPlaceholder')"
            filterable
            @update:value="(val: number | null) => emit('update:executionPlanId', val)"
          />
        </NFormItem>
        <NFormItem :label="t('workspace.job.form.autoApprove')" path="auto_approve">
          <div class="flex items-center gap-3">
            <NSwitch
              :value="autoApprove"
              @update:value="(val: boolean) => emit('update:autoApprove', val)"
            />
            <span class="text-sm text-lf-text-muted">
              {{ t('workspace.job.form.autoApproveHint') }}
            </span>
          </div>
        </NFormItem>
        <NFormItem :label="t('workspace.job.form.segmentFilter')" path="segment_filter">
          <div class="w-full">
            <div class="flex items-center gap-3">
              <NSwitch
                :value="overrideSegmentFilter"
                @update:value="(val: boolean) => handleToggleOverride(val)"
              />
              <span class="text-sm text-lf-text-muted">
                {{ t('workspace.job.form.segmentFilterOverride') }}
              </span>
            </div>
            <NRadioGroup
              v-if="overrideSegmentFilter"
              class="mt-3"
              :value="segmentFilter"
              @update:value="(val: SegmentFilter) => emit('update:segmentFilter', val)"
            >
              <NSpace vertical>
                <NRadio
                  v-for="option in segmentFilterOptions"
                  :key="option.value"
                  :value="option.value"
                >
                  {{ t(option.labelKey) }}
                </NRadio>
              </NSpace>
            </NRadioGroup>
            <div v-else class="mt-2 text-xs text-lf-text-subtle">
              {{ t('workspace.job.form.segmentFilterFollowHint') }}
            </div>
            <div class="mt-2 text-xs text-lf-text-subtle">
              {{ t('workspace.job.form.segmentFilterScopeHint') }}
            </div>
          </div>
        </NFormItem>
      </NForm>

      <!-- 执行计划详情预览 -->
      <NCard
        v-if="selectedPlanTemplate"
        :title="t('workspace.job.planPreviewTitle')"
        size="small"
        :bordered="true"
        class="mb-4"
      >
        <div class="space-y-2 text-sm">
          <div class="font-medium text-lf-text-strong">
            {{ selectedPlanTemplate.name }}
          </div>
          <div v-if="selectedPlanTemplate.description" class="text-lf-text-muted">
            {{ selectedPlanTemplate.description }}
          </div>
          <div class="text-lf-text-muted">
            {{
              t('workspace.job.planPreviewRounds', {
                count: selectedPlanTemplate.rounds?.length ?? 0,
              })
            }}
          </div>
          <ul v-if="selectedPlanTemplate.rounds?.length" class="list-none space-y-1 pl-0">
            <li
              v-for="(round, index) in selectedPlanTemplate.rounds"
              :key="index"
              class="text-lf-text-muted"
            >
              {{ formatRoundSummary(round, index) }}
            </li>
          </ul>
        </div>
      </NCard>

      <!-- 确认步骤摘要 -->
      <NAlert
        v-if="executionPlanId && selectedPlanTemplate"
        type="warning"
        :bordered="false"
        class="mb-2"
      >
        {{
          t('workspace.job.confirmSummary', {
            planName: selectedPlanTemplate.name,
            resourceCount: targetResourceIds.length,
            segmentCount: segmentCount,
          })
        }}
      </NAlert>

      <template #footer>
        <div class="flex justify-end gap-3">
          <NButton :disabled="submitting" @click="emit('close')">
            {{ t('workspace.common.cancel') }}
          </NButton>
          <NButton
            type="primary"
            :disabled="!executionPlanId"
            :loading="submitting"
            @click="emit('submit')"
          >
            {{ t('workspace.job.actions.submitCreate') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
