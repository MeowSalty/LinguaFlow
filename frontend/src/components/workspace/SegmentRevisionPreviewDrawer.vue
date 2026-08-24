<script setup lang="ts">
import {
  NAlert,
  NButton,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NFormItem,
  NInput,
  NSelect,
  NTag,
  useDialog,
  useMessage,
} from 'naive-ui'
import { computed, onMounted, onUnmounted, ref, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'

import {
  applyResourceSegmentTranslationPreview,
  isSegmentTranslationPreviewError,
  previewResourceSegmentRevision,
} from '@/api/projects'
import type { ApiSchemas } from '@/api/client'
import {
  formatQualityIssueTooltip,
  getQualityCodeLabel,
  isIssueDismissed,
  isSemanticRepairIssueCode,
  SEMANTIC_REPAIR_ISSUE_CODES,
} from '@/composables/useQualityIssues'
import { getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

import SegmentTextDisplay from './SegmentTextDisplay.vue'
import SegmentTranslationPreviewDiagnostic from './SegmentTranslationPreviewDiagnostic.vue'

type Segment = ApiSchemas['Segment']
type Preview = ApiSchemas['SegmentRevisionPreviewResponse']
type RevisionIssueCode = NonNullable<
  ApiSchemas['SegmentRevisionPreviewRequest']['issue_codes']
>[number]
type DrawerState = 'idle' | 'previewing' | 'ready' | 'failed' | 'applying' | 'applied'

const props = defineProps<{
  projectId: number | null
  textRenderMode: 'plaintext' | 'html'
}>()

const emit = defineEmits<{
  applied: [payload: { segment: Segment; resourceId: number }]
}>()

const show = defineModel<boolean>('show', { default: false })
const dialog = useDialog()
const message = useMessage()
const { t } = useI18n()
const templatesStore = useExecutionPlanTemplatesStore()
const workspace = useProjectWorkspaceStore()

const state = ref<DrawerState>('idle')
const segment = shallowRef<Segment | null>(null)
const resourceId = ref<number | null>(null)
const preview = shallowRef<Preview | null>(null)
const selectedPlanId = ref<number | null>(null)
const selectedIssueCodes = ref<RevisionIssueCode[] | null>(null)
const stalePlan = ref(false)
const errorMessage = ref<string | null>(null)
const errorStatus = ref<number | null>(null)
const retryAfterSeconds = ref<number | null>(null)
const draftTargetText = ref('')
const appliedSegment = shallowRef<Segment | null>(null)
const now = ref(Date.now())
const requestSequence = ref(0)
let previewController: AbortController | null = null
let timer: ReturnType<typeof setInterval> | null = null

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

const hasTarget = computed(() => Boolean(draftTargetText.value.trim()))
const hasToken = computed(() => Boolean(preview.value?.apply_token))
const tokenExpired = computed(() => {
  const expiresAt = preview.value?.apply_expires_at
  return Boolean(expiresAt && new Date(expiresAt).getTime() <= now.value)
})
const canApply = computed(() =>
  Boolean(
    props.projectId &&
    resourceId.value &&
    segment.value &&
    hasTarget.value &&
    hasToken.value &&
    !tokenExpired.value &&
    !stalePlan.value &&
    (preview.value?.status === 'success' || preview.value?.status === 'partial') &&
    state.value !== 'previewing' &&
    state.value !== 'applying' &&
    state.value !== 'applied',
  ),
)
const busy = computed(() => state.value === 'previewing' || state.value === 'applying')
const currentTargetText = computed(
  () => appliedSegment.value?.target_text ?? segment.value?.target_text,
)
const sourceText = computed(() => preview.value?.source_text ?? segment.value?.source_text ?? '')
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
const executionSummary = computed(() => {
  const execution = preview.value?.execution
  if (!execution) return ''
  return t('workspace.segment.translationPreview.executionSummary', {
    name: execution.execution_plan_name,
    rounds: execution.rounds.length,
  })
})

const clearPreview = (): void => {
  preview.value = null
  errorMessage.value = null
  errorStatus.value = null
  retryAfterSeconds.value = null
  draftTargetText.value = ''
  stalePlan.value = false
  appliedSegment.value = null
  state.value = 'idle'
}

const open = (nextSegment: Segment, nextResourceId: number): void => {
  previewController?.abort()
  requestSequence.value++
  segment.value = { ...nextSegment }
  resourceId.value = nextResourceId
  selectedPlanId.value = null
  selectedIssueCodes.value = null
  clearPreview()
  show.value = true
  if (templatesStore.items.length === 0) {
    void templatesStore.loadTemplates()
  }
}

const handlePlanChange = (value: number | null): void => {
  selectedPlanId.value = value
  if (preview.value) {
    stalePlan.value = true
  }
}

const startPreview = async (): Promise<void> => {
  if (!props.projectId || !resourceId.value || !segment.value || !selectedPlanId.value) return

  previewController?.abort()
  const controller = new AbortController()
  previewController = controller
  const sequence = ++requestSequence.value
  state.value = 'previewing'
  errorMessage.value = null
  errorStatus.value = null
  retryAfterSeconds.value = null
  stalePlan.value = false
  appliedSegment.value = null

  try {
    const result = await previewResourceSegmentRevision(
      props.projectId,
      resourceId.value,
      segment.value.id,
      selectedPlanId.value,
      selectedIssueCodes.value ?? undefined,
      controller.signal,
    )
    if (sequence !== requestSequence.value || controller.signal.aborted) return
    preview.value = result
    draftTargetText.value = result.target_text ?? ''
    state.value = result.status === 'failed' ? 'failed' : 'ready'
  } catch (error) {
    if (sequence !== requestSequence.value || controller.signal.aborted) return
    errorStatus.value = isSegmentTranslationPreviewError(error) ? error.status : null
    retryAfterSeconds.value = isSegmentTranslationPreviewError(error)
      ? (error.retryAfterSeconds ?? null)
      : null
    errorMessage.value =
      error instanceof Error ? error.message : t('api.errors.previewSegmentRevisionFailed')
    state.value = 'failed'
  } finally {
    if (sequence === requestSequence.value) {
      previewController = null
    }
  }
}

const handleApply = async (): Promise<void> => {
  if (
    !canApply.value ||
    !props.projectId ||
    !resourceId.value ||
    !segment.value ||
    !preview.value?.apply_token
  ) {
    return
  }

  state.value = 'applying'
  errorMessage.value = null
  try {
    const result = await applyResourceSegmentTranslationPreview(
      props.projectId,
      resourceId.value,
      segment.value.id,
      preview.value.apply_token,
      draftTargetText.value,
    )
    appliedSegment.value = result
    state.value = 'applied'
    message.success(t('workspace.segment.revisionPreview.applySuccess'))
    emit('applied', { segment: result, resourceId: resourceId.value })
  } catch (error) {
    const knownError = isSegmentTranslationPreviewError(error) ? error : null
    errorStatus.value = knownError?.status ?? null
    errorMessage.value =
      error instanceof Error ? error.message : t('api.errors.applySegmentTranslationPreviewFailed')
    if (knownError?.status === 409 || knownError?.status === 410) {
      preview.value = preview.value ? { ...preview.value, apply_token: undefined } : null
    }
    state.value = 'ready'
  }
}

const close = (): void => {
  show.value = false
  previewController?.abort()
  previewController = null
  requestSequence.value++
}

const requestClose = (): void => {
  if (state.value === 'applying') return
  if (state.value !== 'previewing') {
    close()
    return
  }

  dialog.warning({
    title: t('workspace.segment.revisionPreview.closeTitle'),
    content: t('workspace.segment.revisionPreview.closeConfirm'),
    positiveText: t('workspace.common.confirm'),
    negativeText: t('workspace.common.cancel'),
    onPositiveClick: () => close(),
  })
}

const statusLabel = (status: Preview['status']): string =>
  t(`workspace.segment.translationPreview.status.${status}`)

defineExpose({ open })

onMounted(() => {
  timer = setInterval(() => {
    now.value = Date.now()
  }, 1000)
})

onUnmounted(() => {
  previewController?.abort()
  if (timer) clearInterval(timer)
})
</script>

<template>
  <NDrawer
    :show="show"
    placement="right"
    :width="'min(800px, 100vw)'"
    :mask-closable="false"
    :close-on-esc="false"
    @update:show="(value) => (value ? (show = true) : requestClose())"
  >
    <NDrawerContent
      :title="t('workspace.segment.revisionPreview.title')"
      closable
      @close="requestClose"
    >
      <div v-if="segment" class="space-y-4 pb-4">
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-semibold text-lf-text-strong">
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
            class="max-h-48 overflow-auto rounded-lg border border-lf-border-soft bg-lf-surface-muted/40 p-3 text-sm leading-6"
          >
            <SegmentTextDisplay :text="sourceText" :mode="textRenderMode" />
          </div>
        </section>

        <section v-if="currentTargetText">
          <div class="mb-1.5 text-xs font-medium text-lf-text-muted">
            {{ t('workspace.segment.revisionPreview.originalTarget') }}
          </div>
          <div
            class="max-h-40 overflow-auto rounded-lg border border-lf-border-soft bg-lf-surface-muted/40 p-3 text-sm leading-6"
          >
            <SegmentTextDisplay :text="currentTargetText" :mode="textRenderMode" />
          </div>
        </section>

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

        <NAlert v-if="templatesStore.error" type="error" :bordered="false">
          {{ templatesStore.error }}
        </NAlert>
        <NAlert v-if="state === 'previewing'" type="info" :bordered="false">
          {{ t('workspace.segment.revisionPreview.previewing') }}
        </NAlert>
        <NAlert v-if="stalePlan" type="warning" :bordered="false">
          {{ t('workspace.segment.revisionPreview.stalePlan') }}
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
            {{ t('workspace.segment.revisionPreview.conflict') }}
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
            {{ t('workspace.segment.revisionPreview.failed') }}
          </NAlert>
          <NAlert v-if="unchanged && preview.status !== 'failed'" type="info" :bordered="false">
            {{ t('workspace.segment.revisionPreview.unchanged') }}
          </NAlert>

          <div class="flex flex-wrap gap-2 text-xs text-lf-text-muted">
            <span v-if="executionSummary">{{ executionSummary }}</span>
            <span v-if="usageSummary">{{ usageSummary }}</span>
          </div>
          <div v-if="preview.execution.rounds.length" class="flex flex-wrap gap-1.5">
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
          <NAlert v-if="tokenExpired" type="warning" :bordered="false">
            {{ t('workspace.segment.translationPreview.tokenExpired') }}
          </NAlert>
          <div v-if="preview.apply_expires_at" class="text-xs text-lf-text-muted">
            {{
              t('workspace.segment.translationPreview.expiresAt', {
                time: new Intl.DateTimeFormat('zh-Hans', {
                  dateStyle: 'medium',
                  timeStyle: 'short',
                }).format(new Date(preview.apply_expires_at)),
              })
            }}
          </div>

          <section v-if="preview.fix_issues?.length" class="space-y-2">
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

          <section v-if="preview.status !== 'failed'">
            <div class="mb-1.5 text-xs font-medium text-lf-text-muted">
              {{ t('workspace.segment.revisionPreview.result') }}
            </div>
            <NInput
              v-model:value="draftTargetText"
              type="textarea"
              :autosize="{ minRows: 5, maxRows: 14 }"
              :disabled="state === 'applying' || state === 'applied'"
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
        <NEmpty
          v-else-if="state === 'idle'"
          :description="t('workspace.segment.revisionPreview.empty')"
        />
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
            @click="handleApply"
          >
            {{ t('workspace.segment.revisionPreview.apply') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
