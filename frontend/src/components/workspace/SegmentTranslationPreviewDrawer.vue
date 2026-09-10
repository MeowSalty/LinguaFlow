<script setup lang="ts">
import { NButton, NFormItem, NSelect } from 'naive-ui'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { previewResourceSegmentTranslation } from '@/api/projects'
import type { ApiSchemas } from '@/api/client'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useSegmentPreviewDrawer } from '@/composables/useSegmentPreviewDrawer'

import SegmentPreviewDrawerBase from './SegmentPreviewDrawerBase.vue'

type Segment = ApiSchemas['Segment']
type Preview = ApiSchemas['SegmentTranslationPreviewResponse']

const props = defineProps<{
  projectId: number | null
  textRenderMode: 'plaintext' | 'html'
}>()

const emit = defineEmits<{
  applied: [payload: { segment: Segment; resourceId: number }]
}>()

const show = defineModel<boolean>('show', { default: false })

const { t } = useI18n()
const templatesStore = useExecutionPlanTemplatesStore()

const {
  state,
  segment,
  preview,
  selectedPlanId,
  stalePlan,
  errorMessage,
  errorStatus,
  retryAfterSeconds,
  draftTargetText,
  appliedSegment,
  canApply,
  busy,
  tokenExpired,
  currentTargetText,
  sourceText,
  executionSummary,
  open,
  handlePlanChange,
  startPreview,
  handleApply,
  requestClose,
} = useSegmentPreviewDrawer<Preview>({
  show,
  projectId: () => props.projectId,
  requestPreview: ({ projectId, resourceId, segmentId, executionPlanId, signal }) =>
    previewResourceSegmentTranslation(projectId, resourceId, segmentId, executionPlanId, signal),
  previewFallbackErrorKey: 'api.errors.previewSegmentTranslationFailed',
  applySuccessKey: 'workspace.segment.translationPreview.applySuccess',
  closeTitleKey: 'workspace.segment.translationPreview.closeTitle',
  closeConfirmKey: 'workspace.segment.translationPreview.closeConfirm',
  onApplied: (payload) => emit('applied', payload),
})

const planOptions = computed(() =>
  templatesStore.items
    .filter((item) => item.rounds?.some((round) => round.mode === 'translate'))
    .map((item) => ({
      label: t('workspace.job.executionPlanLabel', {
        name: item.name,
        rounds: item.rounds?.length ?? 0,
      }),
      value: item.id,
    })),
)

const applyButtonText = computed(() =>
  preview.value?.status === 'partial'
    ? t('workspace.segment.translationPreview.applyPartial')
    : t('workspace.segment.translationPreview.apply'),
)

defineExpose({ open })
</script>

<template>
  <SegmentPreviewDrawerBase
    v-model:show="show"
    v-model:target-text="draftTargetText"
    :title="t('workspace.segment.translationPreview.title')"
    :segment="segment"
    :applied-segment="appliedSegment"
    :text-render-mode="textRenderMode"
    :source-text="sourceText"
    :current-target-text="currentTargetText"
    :current-target-label="t('workspace.segment.translationPreview.currentTarget')"
    :state="state"
    :can-apply="canApply"
    :apply-button-text="applyButtonText"
    :preview="preview"
    :token-expired="tokenExpired"
    :preview-failed-text="t('workspace.segment.translationPreview.failed')"
    :conflict-text="t('workspace.segment.translationPreview.conflict')"
    :result-label="t('workspace.segment.translationPreview.result')"
    :result-visible="true"
    :result-disabled="state === 'applying' || state === 'applied' || preview?.status === 'failed'"
    :empty-text="t('workspace.segment.translationPreview.empty')"
    :previewing-text="t('workspace.segment.translationPreview.previewing')"
    :stale-plan="stalePlan"
    :stale-plan-text="t('workspace.segment.translationPreview.stalePlan')"
    :templates-error="templatesStore.error"
    :error-message="errorMessage"
    :error-status="errorStatus"
    :retry-after-seconds="retryAfterSeconds"
    @request-close="requestClose"
    @apply="handleApply"
  >
    <template #form>
      <section class="space-y-3">
        <NFormItem :label="t('workspace.segment.translationPreview.planLabel')">
          <NSelect
            :value="selectedPlanId"
            :options="planOptions"
            :loading="templatesStore.loading"
            :placeholder="t('workspace.segment.translationPreview.planPlaceholder')"
            filterable
            :disabled="busy || state === 'applied'"
            @update:value="handlePlanChange"
          />
        </NFormItem>
        <NButton
          type="primary"
          :loading="state === 'previewing'"
          :disabled="!selectedPlanId || state === 'applying' || state === 'applied'"
          @click="startPreview"
        >
          {{
            state === 'ready' || state === 'failed'
              ? t('workspace.segment.translationPreview.retry')
              : t('workspace.segment.translationPreview.start')
          }}
        </NButton>
      </section>
    </template>

    <template #preview-summary>
      <div v-if="executionSummary" class="text-xs text-lf-text-muted">
        {{ executionSummary }}
      </div>
    </template>
  </SegmentPreviewDrawerBase>
</template>
