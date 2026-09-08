<script setup lang="ts">
import { NAlert, NButton, NDrawer, NDrawerContent, NEmpty, NInput, NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import { formatQualityIssueTooltip, getQualityCodeLabel } from '@/composables/useQualityIssues'
import type {
  SegmentPreviewDrawerState,
  SegmentPreviewShared,
} from '@/composables/useSegmentPreviewDrawer'
import { getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { formatDateTime } from '@/utils/datetime'

import SegmentTextDisplay from './SegmentTextDisplay.vue'
import SegmentTranslationPreviewDiagnostic from './SegmentTranslationPreviewDiagnostic.vue'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

type Segment = ApiSchemas['Segment']

defineProps<{
  title: string
  segment: Segment | null
  appliedSegment: Segment | null
  textRenderMode: 'plaintext' | 'html'
  sourceText: string
  currentTargetText?: string | null
  currentTargetLabel: string
  state: SegmentPreviewDrawerState
  canApply: boolean
  applyButtonText: string
  preview: SegmentPreviewShared | null
  tokenExpired: boolean
  previewFailedText: string
  conflictText: string
  resultLabel: string
  resultVisible: boolean
  resultDisabled: boolean
  emptyText: string
  previewingText: string
  stalePlan: boolean
  stalePlanText: string
  templatesError?: string | null
  errorMessage?: string | null
  errorStatus?: number | null
  retryAfterSeconds?: number | null
}>()

const emit = defineEmits<{
  'request-close': []
  apply: []
}>()

const show = defineModel<boolean>('show', { default: false })
const targetText = defineModel<string>('targetText', { default: '' })

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

const statusLabel = (status: SegmentPreviewShared['status']): string =>
  t(`workspace.segment.translationPreview.status.${status}`)

const expiresAtText = (expiresAt: string): string =>
  t('workspace.segment.translationPreview.expiresAt', {
    time: formatDateTime(expiresAt, { dateStyle: 'medium', timeStyle: 'short' }),
  })
</script>

<template>
  <NDrawer
    :show="show"
    placement="right"
    :width="DRAWER_WIDTH.l"
    :mask-closable="false"
    :close-on-esc="false"
    @update:show="(value) => (value ? (show = true) : emit('request-close'))"
  >
    <NDrawerContent :title="title" closable @close="emit('request-close')">
      <div v-if="segment" class="space-y-4 pb-4">
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-semibold text-lf-text-strong tabular-nums">
            #{{ segment.segment_index }}
          </span>
          <NTag size="small" :type="statusTagType(appliedSegment?.status ?? segment.status)">
            {{ getSegmentStatusLabel(appliedSegment?.status ?? segment.status) }}
          </NTag>
          <span class="text-xs text-lf-text-muted">
            {{ workspace.project?.source_lang || '-' }} →
            {{ workspace.project?.target_lang || '-' }}
          </span>
        </div>

        <section>
          <div class="mb-1.5 text-xs font-medium text-lf-text-muted">
            {{ t('workspace.segment.translationPreview.source') }}
          </div>
          <div
            class="max-h-48 overflow-auto rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/40 p-3 text-sm leading-6"
          >
            <SegmentTextDisplay :text="sourceText" :mode="textRenderMode" />
          </div>
        </section>

        <section v-if="currentTargetText">
          <div class="mb-1.5 text-xs font-medium text-lf-text-muted">
            {{ currentTargetLabel }}
          </div>
          <div
            class="max-h-40 overflow-auto rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/40 p-3 text-sm leading-6"
          >
            <SegmentTextDisplay :text="currentTargetText" :mode="textRenderMode" />
          </div>
        </section>

        <slot name="form" />

        <NAlert v-if="templatesError" type="error" :bordered="false">
          {{ templatesError }}
        </NAlert>
        <NAlert v-if="state === 'previewing'" type="info" :bordered="false">
          {{ previewingText }}
        </NAlert>
        <NAlert v-if="stalePlan" type="warning" :bordered="false">
          {{ stalePlanText }}
        </NAlert>
        <NAlert v-if="errorMessage" type="error" :bordered="false">
          <div>{{ errorMessage }}</div>
          <div v-if="errorStatus === 429" class="mt-1 text-sm">
            {{
              retryAfterSeconds != null
                ? t('workspace.segment.translationPreview.rateLimitedWithRetry', {
                    seconds: retryAfterSeconds,
                  })
                : t('workspace.segment.translationPreview.rateLimited')
            }}
          </div>
          <div v-if="errorStatus === 409" class="mt-1 text-sm">
            {{ conflictText }}
          </div>
          <div v-if="errorStatus === 410" class="mt-1 text-sm">
            {{ t('workspace.segment.translationPreview.tokenExpired') }}
          </div>
        </NAlert>

        <template v-if="preview">
          <NAlert v-if="preview.status === 'success'" type="success" :bordered="false">
            {{ statusLabel(preview.status) }}
          </NAlert>
          <NAlert v-else-if="preview.status === 'partial'" type="warning" :bordered="false">
            {{ t('workspace.segment.translationPreview.partialWarning') }}
          </NAlert>
          <NAlert v-else type="error" :bordered="false">
            {{ previewFailedText }}
          </NAlert>

          <slot name="preview-summary" />

          <NAlert v-if="tokenExpired" type="warning" :bordered="false">
            {{ t('workspace.segment.translationPreview.tokenExpired') }}
          </NAlert>
          <div v-if="preview.apply_expires_at" class="text-xs text-lf-text-muted tabular-nums">
            {{ expiresAtText(preview.apply_expires_at) }}
          </div>

          <slot name="preview-extra" />

          <section v-if="resultVisible">
            <div class="mb-1.5 text-xs font-medium text-lf-text-muted">
              {{ resultLabel }}
            </div>
            <NInput
              v-model:value="targetText"
              type="textarea"
              :autosize="{ minRows: 5, maxRows: 14 }"
              :disabled="resultDisabled"
            />
          </section>

          <section v-if="preview.quality_issues?.length" class="space-y-2">
            <div class="text-xs font-medium text-lf-text-muted">
              {{ t('workspace.segment.translationPreview.qualityIssues') }}
            </div>
            <div class="flex flex-wrap gap-1.5">
              <NTag
                v-for="(issue, index) in preview.quality_issues"
                :key="`${issue.code}-${index}`"
                size="small"
                :type="issue.severity === 'error' ? 'error' : 'warning'"
                :title="formatQualityIssueTooltip(issue)"
              >
                {{ getQualityCodeLabel(issue.code) }}
              </NTag>
            </div>
          </section>

          <section v-if="preview.batches.length" class="space-y-2">
            <div class="text-xs font-medium text-lf-text-muted">
              {{ t('workspace.segment.translationPreview.diagnostics') }}
            </div>
            <SegmentTranslationPreviewDiagnostic
              v-for="(batch, index) in preview.batches"
              :key="`${batch.stage}-${batch.attempt ?? 0}-${index}`"
              :batch="batch"
              :index="index"
            />
          </section>
        </template>
        <NEmpty v-else-if="state === 'idle'" :description="emptyText" />
      </div>

      <template #footer>
        <div class="flex items-center justify-between gap-3">
          <span v-if="state === 'applied'" class="text-sm text-lf-success">
            {{ t('workspace.segment.translationPreview.applied') }}
          </span>
          <span v-else />
          <NButton
            type="primary"
            :loading="state === 'applying'"
            :disabled="!canApply"
            @click="emit('apply')"
          >
            {{ applyButtonText }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
