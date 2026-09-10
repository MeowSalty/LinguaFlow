<script setup lang="ts">
import { NButton, NFormItem, NSelect, NTag } from 'naive-ui'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { previewResourceSegmentRevision } from '@/api/projects'
import type { ApiSchemas } from '@/api/client'
import {
  formatQualityIssueTooltip,
  getQualityCodeLabel,
  isIssueDismissed,
  isSemanticRepairIssueCode,
  SEMANTIC_REPAIR_ISSUE_CODES,
} from '@/composables/useQualityIssues'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useSegmentPreviewDrawer } from '@/composables/useSegmentPreviewDrawer'

import SegmentPreviewDrawerBase from './SegmentPreviewDrawerBase.vue'

type Segment = ApiSchemas['Segment']
type Preview = ApiSchemas['SegmentRevisionPreviewResponse']
type RevisionIssueCode = NonNullable<
  ApiSchemas['SegmentRevisionPreviewRequest']['issue_codes']
>[number]

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

const selectedIssueCodes = ref<RevisionIssueCode[] | null>(null)

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
  open: openDrawer,
  handlePlanChange,
  startPreview,
  handleApply,
  requestClose,
} = useSegmentPreviewDrawer<Preview>({
  show,
  projectId: () => props.projectId,
  requestPreview: ({ projectId, resourceId, segmentId, executionPlanId, signal }) =>
    previewResourceSegmentRevision(
      projectId,
      resourceId,
      segmentId,
      executionPlanId,
      selectedIssueCodes.value ?? undefined,
      signal,
    ),
  previewFallbackErrorKey: 'api.errors.previewSegmentRevisionFailed',
  applySuccessKey: 'workspace.segment.revisionPreview.applySuccess',
  closeTitleKey: 'workspace.segment.revisionPreview.closeTitle',
  closeConfirmKey: 'workspace.segment.revisionPreview.closeConfirm',
  onApplied: (payload) => emit('applied', payload),
})

const planOptions = computed(() =>
  templatesStore.items
    .filter((item) =>
      item.rounds?.some((round) => round.mode === 'revise' || round.mode === 'translate'),
    )
    .map((item) => ({
      label: t('workspace.job.executionPlanLabel', {
        name: item.name,
        rounds: item.rounds?.length ?? 0,
      }),
      value: item.id,
    })),
)

const issueCodeOptions = computed(() =>
  SEMANTIC_REPAIR_ISSUE_CODES.map((value) => ({
    label: getQualityCodeLabel(value),
    value: value as RevisionIssueCode,
  })),
)

/** 段落上当前 pending 的语义 issue（修订的潜在修复目标） */
const pendingSemanticIssues = computed(() =>
  (segment.value?.quality_issues ?? []).filter(
    (issue) => !isIssueDismissed(issue) && isSemanticRepairIssueCode(issue.code),
  ),
)

const canRevise = computed(
  () =>
    Boolean(
      segment.value &&
      (segment.value.status === 'translated' || segment.value.status === 'edited') &&
      segment.value.target_text,
    ) && pendingSemanticIssues.value.length > 0,
)

const unchanged = computed(
  () =>
    preview.value != null &&
    preview.value.target_text != null &&
    preview.value.target_text === preview.value.original_target_text,
)
const usageSummary = computed(() => {
  const usage = preview.value?.usage
  if (!usage) return ''
  return t('workspace.segment.revisionPreview.usageSummary', {
    calls: usage.api_calls,
    input: usage.input_tokens,
    output: usage.output_tokens,
  })
})

const open = (nextSegment: Segment, nextResourceId: number): void => {
  selectedIssueCodes.value = null
  openDrawer(nextSegment, nextResourceId)
}

defineExpose({ open })
</script>

<template>
  <SegmentPreviewDrawerBase
    v-model:show="show"
    v-model:target-text="draftTargetText"
    :title="t('workspace.segment.revisionPreview.title')"
    :segment="segment"
    :applied-segment="appliedSegment"
    :text-render-mode="textRenderMode"
    :source-text="sourceText"
    :current-target-text="currentTargetText"
    :current-target-label="t('workspace.segment.revisionPreview.originalTarget')"
    :state="state"
    :can-apply="canApply"
    :apply-button-text="t('workspace.segment.revisionPreview.apply')"
    :preview="preview"
    :token-expired="tokenExpired"
    :preview-failed-text="t('workspace.segment.revisionPreview.failed')"
    :conflict-text="t('workspace.segment.revisionPreview.conflict')"
    :result-label="t('workspace.segment.revisionPreview.result')"
    :result-visible="preview?.status !== 'failed'"
    :result-disabled="state === 'applying' || state === 'applied'"
    :empty-text="t('workspace.segment.revisionPreview.empty')"
    :previewing-text="t('workspace.segment.revisionPreview.previewing')"
    :stale-plan="stalePlan"
    :stale-plan-text="t('workspace.segment.revisionPreview.stalePlan')"
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
            :placeholder="t('workspace.segment.revisionPreview.planPlaceholder')"
            filterable
            :disabled="busy || state === 'applied'"
            @update:value="handlePlanChange"
          />
        </NFormItem>
        <NFormItem :label="t('workspace.segment.revisionPreview.issueCodesLabel')">
          <NSelect
            v-model:value="selectedIssueCodes"
            :options="issueCodeOptions"
            multiple
            clearable
            :placeholder="t('workspace.segment.revisionPreview.issueCodesPlaceholder')"
            :disabled="busy || state === 'applied'"
          />
          <template #feedback>
            {{ t('workspace.segment.revisionPreview.issueCodesHint') }}
          </template>
        </NFormItem>
        <NButton
          type="primary"
          :loading="state === 'previewing'"
          :disabled="!selectedPlanId || !canRevise || state === 'applying' || state === 'applied'"
          @click="startPreview"
        >
          {{
            state === 'ready' || state === 'failed'
              ? t('workspace.segment.revisionPreview.retry')
              : t('workspace.segment.revisionPreview.start')
          }}
        </NButton>
      </section>

      <NAlert v-if="!canRevise" type="warning" :bordered="false">
        {{ t('workspace.segment.revisionPreview.precondition') }}
      </NAlert>

      <section v-if="pendingSemanticIssues.length" class="space-y-2">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('workspace.segment.revisionPreview.pendingIssues') }}
        </div>
        <div class="flex flex-wrap gap-1.5">
          <NTag
            v-for="(issue, index) in pendingSemanticIssues"
            :key="`${issue.code}-${index}`"
            size="small"
            :type="issue.severity === 'error' ? 'error' : 'warning'"
            :title="formatQualityIssueTooltip(issue)"
          >
            {{ getQualityCodeLabel(issue.code) }}
          </NTag>
        </div>
      </section>
    </template>

    <template #preview-summary>
      <NAlert v-if="unchanged && preview?.status !== 'failed'" type="info" :bordered="false">
        {{ t('workspace.segment.revisionPreview.unchanged') }}
      </NAlert>

      <div class="flex flex-wrap gap-2 text-xs text-lf-text-muted">
        <span v-if="executionSummary">{{ executionSummary }}</span>
        <span v-if="usageSummary">{{ usageSummary }}</span>
      </div>
      <div v-if="preview?.execution.rounds.length" class="flex flex-wrap gap-1.5">
        <NTag
          v-for="round in preview.execution.rounds"
          :key="round.index"
          size="small"
          :bordered="false"
        >
          {{ t('workspace.segment.revisionPreview.roundItem', { index: round.index + 1 }) }}
          <template v-if="round.synthesized">
            · {{ t('workspace.segment.revisionPreview.synthesizedRound') }}
          </template>
        </NTag>
      </div>
    </template>

    <template #preview-extra>
      <section v-if="preview?.fix_issues?.length" class="space-y-2">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('workspace.segment.revisionPreview.fixIssues') }}
        </div>
        <div class="flex flex-wrap gap-1.5">
          <NTag
            v-for="(issue, index) in preview.fix_issues"
            :key="`${issue.code}-${index}`"
            size="small"
            :type="issue.severity === 'error' ? 'error' : 'warning'"
            :title="formatQualityIssueTooltip(issue)"
          >
            {{ getQualityCodeLabel(issue.code) }}
          </NTag>
        </div>
      </section>
    </template>
  </SegmentPreviewDrawerBase>
</template>
