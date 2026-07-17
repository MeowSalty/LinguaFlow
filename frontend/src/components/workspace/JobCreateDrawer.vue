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
type CreateJobRequest = ApiSchemas['CreateJobRequest']
type OverwriteMode = CreateJobRequest['overwrite_mode']

const { t } = useI18n()
const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()

const show = defineModel<boolean>('show', { default: false })

defineProps<{
  formRef: FormInst | null
  targetMode: JobTargetMode
  targetResourceIds: number[]
  targetSegmentIds: number[]
  targetGroupKeys: string[]
  executionPlanId: number | null
  autoApprove: boolean
  overwriteMode: OverwriteMode
  formRules: FormRules
  executionPlanOptions: Array<{ label: string; value: number }>
  submitting: boolean
  segmentCount: number
  selectedPlanTemplate: ExecutionPlanTemplate | null
}>()

const emit = defineEmits<{
  'update:executionPlanId': [value: number | null]
  'update:autoApprove': [value: boolean]
  'update:overwriteMode': [value: OverwriteMode]
  submit: []
  close: []
}>()

const overwriteModeOptions = [
  { value: 'skip_translated', labelKey: 'workspace.job.overwriteMode.skipTranslated' },
  { value: 'overwrite_unapproved', labelKey: 'workspace.job.overwriteMode.overwriteUnapproved' },
  { value: 'overwrite_all', labelKey: 'workspace.job.overwriteMode.overwriteAll' },
]
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
        <NFormItem :label="t('workspace.job.form.overwriteMode')" path="overwrite_mode">
          <NRadioGroup
            :value="overwriteMode"
            @update:value="(val: OverwriteMode) => emit('update:overwriteMode', val)"
          >
            <NSpace vertical>
              <NRadio
                v-for="option in overwriteModeOptions"
                :key="option.value"
                :value="option.value"
              >
                {{ t(option.labelKey) }}
              </NRadio>
            </NSpace>
          </NRadioGroup>
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
              {{
                t('workspace.job.planPreviewRoundItem', {
                  index: index + 1,
                  batchSize: round.translate?.batch_size ?? 0,
                  maxWordsPerBatch: round.translate?.max_words_per_batch ?? 0,
                  concurrency: round.concurrency,
                })
              }}
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
