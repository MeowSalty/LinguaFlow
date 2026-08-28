<script setup lang="ts">
import {
  NAlert,
  NButton,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NFormItem,
  NInput,
  NRadioButton,
  NRadioGroup,
  NTag,
  useDialog,
  useMessage,
} from 'naive-ui'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import {
  applyResourceSegmentsSearchReplace,
  isSearchReplaceApplyError,
  previewResourceSegmentsSearchReplace,
  undoResourceSegmentsSearchReplace,
  type SearchReplaceMatchMode,
} from '@/api/projects'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

import SegmentTextDisplay from './SegmentTextDisplay.vue'

type PreviewResponse = ApiSchemas['SearchReplacePreviewResponse']

const props = defineProps<{
  projectId: number | null
  textRenderMode: 'plaintext' | 'html'
  selectedSegmentIds: number[]
}>()

const emit = defineEmits<{
  applied: [payload: { resourceId: number }]
}>()

const show = defineModel<boolean>('show', { default: false })
const dialog = useDialog()
const message = useMessage()
const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

// ── 表单状态 ──
const findText = ref('')
const replaceText = ref('')
const matchMode = ref<SearchReplaceMatchMode>('substring')
const caseSensitive = ref(true)
const wholeWord = ref(false)
const scope = ref<'all' | 'selected'>('all')

// ── 请求/结果状态 ──
const previewing = ref(false)
const applying = ref(false)
const undoing = ref(false)
const errorMessage = ref<string | null>(null)
const errorStatus = ref<number | null>(null)
const preview = ref<PreviewResponse | null>(null)
const previewStale = ref(false)
const lastApplyResult = ref<ApiSchemas['SearchReplaceApplyResponse'] | null>(null)

const resourceId = computed(() => workspace.activeResourceId)
const busy = computed(() => previewing.value || applying.value || undoing.value)
const canSearch = computed(
  () => Boolean(props.projectId && resourceId.value && findText.value.trim()) && !busy.value,
)
const hasPendingChange = computed(() => preview.value !== null || lastApplyResult.value !== null)
const canApply = computed(
  () =>
    preview.value !== null &&
    !previewStale.value &&
    preview.value.matched_segment_count > 0 &&
    !busy.value,
)

/** 作用范围：仅选中段落时把选中 ID 传给预览与应用 */
const scopedSegmentIds = computed<number[] | undefined>(() =>
  scope.value === 'selected' && props.selectedSegmentIds.length > 0
    ? [...props.selectedSegmentIds]
    : undefined,
)

/** 当前资源最近一次搜索替换的 operation_id（可撤销） */
const undoableOperationId = computed(() => workspace.lastSearchReplaceOperationId)

const skippedReasonLabel = (reason: string): string =>
  t(`workspace.segment.searchReplace.skipReasons.${reason}`)

// ── 表单变更后，已有预览结果标记为过期 ──
watch(
  [
    findText,
    replaceText,
    matchMode,
    caseSensitive,
    wholeWord,
    scope,
    () => props.selectedSegmentIds,
  ],
  () => {
    // 选中段落被清空时回退到全部范围，避免静默改变作用域
    if (scope.value === 'selected' && props.selectedSegmentIds.length === 0) {
      scope.value = 'all'
    }
    if (preview.value) {
      previewStale.value = true
    }
  },
)

const open = (): void => {
  show.value = true
}

const close = (): void => {
  show.value = false
}

const requestClose = (): void => {
  if (busy.value) return
  close()
}

const resetResults = (): void => {
  preview.value = null
  previewStale.value = false
  lastApplyResult.value = null
  errorMessage.value = null
  errorStatus.value = null
}

const buildRequest = () => ({
  find: findText.value,
  replace_with: replaceText.value,
  match_mode: matchMode.value,
  case_sensitive: caseSensitive.value,
  whole_word: wholeWord.value,
})

const handlePreview = async (): Promise<void> => {
  if (!canSearch.value || !props.projectId || !resourceId.value) return

  previewing.value = true
  resetResults()

  try {
    preview.value = await previewResourceSegmentsSearchReplace(props.projectId, resourceId.value, {
      ...buildRequest(),
      ...(scopedSegmentIds.value ? { segment_ids: scopedSegmentIds.value } : {}),
    })
  } catch (error) {
    const knownError = isSearchReplaceApplyError(error) ? error : null
    errorStatus.value = knownError?.status ?? null
    errorMessage.value =
      error instanceof Error ? error.message : t('api.errors.previewSearchReplaceFailed')
  } finally {
    previewing.value = false
  }
}

const handleApply = (): void => {
  if (!canApply.value || !props.projectId || !resourceId.value || !preview.value) return

  const { matched_segment_count: matchedCount, total_replacements: replacements } = preview.value

  dialog.warning({
    title: t('workspace.segment.searchReplace.applyConfirmTitle'),
    content: t('workspace.segment.searchReplace.applyConfirmContent', {
      segments: matchedCount,
      replacements,
    }),
    positiveText: t('workspace.common.confirm'),
    negativeText: t('workspace.common.cancel'),
    onPositiveClick: () => {
      void doApply()
    },
  })
}

const doApply = async (): Promise<void> => {
  if (!props.projectId || !resourceId.value) return

  applying.value = true
  errorMessage.value = null
  errorStatus.value = null

  try {
    const result = await applyResourceSegmentsSearchReplace(props.projectId, resourceId.value, {
      ...buildRequest(),
      segment_ids: scopedSegmentIds.value,
    })
    // 应用成功后旧预览样本已过期，清空避免误导再次应用
    preview.value = null
    previewStale.value = false
    lastApplyResult.value = result
    workspace.lastSearchReplaceOperationId = result.operation_id
    message.success(
      t('workspace.segment.searchReplace.applySuccess', {
        applied: result.applied_count,
        skipped: result.skipped_count,
      }),
    )
    emit('applied', { resourceId: resourceId.value })
  } catch (error) {
    const knownError = isSearchReplaceApplyError(error) ? error : null
    errorStatus.value = knownError?.status ?? null
    errorMessage.value =
      error instanceof Error ? error.message : t('api.errors.applySearchReplaceFailed')
  } finally {
    applying.value = false
  }
}

const handleUndo = (): void => {
  if (!props.projectId || !resourceId.value || !undoableOperationId.value) return

  dialog.warning({
    title: t('workspace.segment.searchReplace.undoConfirmTitle'),
    content: t('workspace.segment.searchReplace.undoConfirmContent'),
    positiveText: t('workspace.common.confirm'),
    negativeText: t('workspace.common.cancel'),
    onPositiveClick: () => {
      void doUndo()
    },
  })
}

const doUndo = async (): Promise<void> => {
  if (!props.projectId || !resourceId.value || !undoableOperationId.value) return

  undoing.value = true
  errorMessage.value = null
  errorStatus.value = null

  try {
    const result = await undoResourceSegmentsSearchReplace(
      props.projectId,
      resourceId.value,
      undoableOperationId.value,
    )
    // 撤销本身写入新的历史（可再撤销 = 重做）
    workspace.lastSearchReplaceOperationId = result.undo_operation_id
    message.success(
      t('workspace.segment.searchReplace.undoSuccess', {
        undone: result.undone_count,
        skipped: result.skipped_count,
      }),
    )
    emit('applied', { resourceId: resourceId.value })
  } catch (error) {
    const knownError = isSearchReplaceApplyError(error) ? error : null
    errorStatus.value = knownError?.status ?? null
    if (knownError?.status === 404 || knownError?.status === 409) {
      // 历史不存在或已全部发散，清理本地 operation 记录
      workspace.lastSearchReplaceOperationId = null
    }
    errorMessage.value =
      error instanceof Error ? error.message : t('api.errors.undoSearchReplaceFailed')
  } finally {
    undoing.value = false
  }
}

defineExpose({ open })
</script>

<template>
  <NDrawer
    :show="show"
    placement="right"
    :width="'min(720px, 100vw)'"
    :mask-closable="!busy"
    :close-on-esc="!busy"
    @update:show="(value: boolean) => (value ? (show = true) : requestClose())"
  >
    <NDrawerContent
      :title="t('workspace.segment.searchReplace.title')"
      closable
      @close="requestClose"
    >
      <div class="space-y-4 pb-4">
        <NAlert type="info" :bordered="false" :show-icon="true">
          {{ t('workspace.segment.searchReplace.hint') }}
        </NAlert>

        <section class="space-y-3">
          <NFormItem
            :label="t('workspace.segment.searchReplace.findLabel')"
            :feedback="t('workspace.segment.searchReplace.findHint')"
          >
            <NInput
              v-model:value="findText"
              :placeholder="t('workspace.segment.searchReplace.findPlaceholder')"
              :disabled="busy"
              @keydown.enter.prevent="handlePreview"
            />
          </NFormItem>
          <NFormItem :label="t('workspace.segment.searchReplace.replaceLabel')">
            <NInput
              v-model:value="replaceText"
              :placeholder="t('workspace.segment.searchReplace.replacePlaceholder')"
              :disabled="busy"
            />
          </NFormItem>

          <div class="flex flex-wrap items-center gap-x-6 gap-y-3">
            <div class="flex items-center gap-2">
              <span class="text-sm text-lf-text-muted">
                {{ t('workspace.segment.searchReplace.matchModeLabel') }}
              </span>
              <NRadioGroup v-model:value="matchMode" size="small" :disabled="busy">
                <NRadioButton :value="'substring'">
                  {{ t('workspace.segment.searchReplace.modeSubstring') }}
                </NRadioButton>
                <NRadioButton :value="'regex'">
                  {{ t('workspace.segment.searchReplace.modeRegex') }}
                </NRadioButton>
              </NRadioGroup>
            </div>
            <NCheckbox v-model:checked="caseSensitive" size="small" :disabled="busy">
              {{ t('workspace.segment.searchReplace.caseSensitive') }}
            </NCheckbox>
            <NCheckbox
              v-model:checked="wholeWord"
              size="small"
              :disabled="busy || matchMode === 'regex'"
            >
              {{ t('workspace.segment.searchReplace.wholeWord') }}
            </NCheckbox>
          </div>
          <p v-if="matchMode === 'regex'" class="text-xs text-lf-text-subtle">
            {{ t('workspace.segment.searchReplace.regexHint') }}
          </p>
          <p v-else-if="wholeWord" class="text-xs text-lf-text-subtle">
            {{ t('workspace.segment.searchReplace.wholeWordHint') }}
          </p>

          <div class="flex flex-wrap items-center gap-2">
            <span class="text-sm text-lf-text-muted">
              {{ t('workspace.segment.searchReplace.scopeLabel') }}
            </span>
            <NRadioGroup v-model:value="scope" size="small" :disabled="busy">
              <NRadioButton value="all">
                {{ t('workspace.segment.searchReplace.scopeAll') }}
              </NRadioButton>
              <NRadioButton value="selected" :disabled="busy || selectedSegmentIds.length === 0">
                {{
                  t('workspace.segment.searchReplace.scopeSelected', {
                    count: selectedSegmentIds.length,
                  })
                }}
              </NRadioButton>
            </NRadioGroup>
          </div>

          <div class="flex items-center gap-2">
            <NButton
              type="primary"
              secondary
              :loading="previewing"
              :disabled="!canSearch"
              @click="handlePreview"
            >
              {{ t('workspace.segment.searchReplace.preview') }}
            </NButton>
            <NButton
              v-if="hasPendingChange"
              size="small"
              quaternary
              :disabled="busy"
              @click="resetResults"
            >
              {{ t('workspace.segment.searchReplace.clearResults') }}
            </NButton>
          </div>
        </section>

        <NAlert v-if="errorMessage" type="error" :bordered="false">
          <div>{{ errorMessage }}</div>
          <div v-if="errorStatus === 409" class="mt-1 text-sm">
            {{ t('workspace.segment.searchReplace.undoConflict') }}
          </div>
          <div v-else-if="errorStatus === 404" class="mt-1 text-sm">
            {{ t('workspace.segment.searchReplace.undoNotFound') }}
          </div>
        </NAlert>

        <!-- 预览结果 -->
        <template v-if="preview">
          <section
            class="flex flex-wrap items-center gap-x-4 gap-y-1.5 rounded-xl border border-lf-border-soft bg-lf-surface-muted/50 px-3.5 py-3 text-sm"
          >
            <span>
              <span class="font-semibold text-lf-text-strong">
                {{ preview.matched_segment_count }}
              </span>
              <span class="text-lf-text-muted">
                {{ t('workspace.segment.searchReplace.matchedSegments') }}
              </span>
            </span>
            <span class="h-3.5 w-px bg-lf-border-soft" />
            <span>
              <span class="font-semibold text-lf-text-strong">
                {{ preview.total_replacements }}
              </span>
              <span class="text-lf-text-muted">
                {{ t('workspace.segment.searchReplace.totalReplacements') }}
              </span>
            </span>
            <NButton
              type="primary"
              class="ml-auto"
              :loading="applying"
              :disabled="!canApply"
              @click="handleApply"
            >
              {{ t('workspace.segment.searchReplace.apply') }}
            </NButton>
          </section>

          <NAlert v-if="previewStale" type="warning" :bordered="false">
            {{ t('workspace.segment.searchReplace.stalePreview') }}
          </NAlert>

          <section v-if="preview.items.length" class="space-y-2.5">
            <div class="text-xs font-medium text-lf-text-muted">
              {{ t('workspace.segment.searchReplace.samples') }}
            </div>
            <div
              v-for="item in preview.items"
              :key="item.segment_id"
              class="space-y-1.5 rounded-xl border border-lf-border-soft bg-lf-surface px-3.5 py-3"
            >
              <div class="flex items-center gap-2 text-xs text-lf-text-muted">
                <span class="font-medium">#{{ item.segment_index }}</span>
                <NTag size="tiny" :bordered="false">
                  {{ t('workspace.segment.searchReplace.matchCount', { count: item.match_count }) }}
                </NTag>
              </div>
              <div
                class="max-h-24 overflow-auto rounded-lg bg-lf-surface-muted/40 px-2.5 py-2 text-xs leading-5 text-lf-text-subtle"
              >
                <SegmentTextDisplay :text="item.source_text" :mode="textRenderMode" />
              </div>
              <div class="grid gap-1.5 text-sm leading-6 sm:grid-cols-2">
                <div class="min-w-0">
                  <div
                    class="mb-0.5 text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase"
                  >
                    {{ t('workspace.segment.searchReplace.before') }}
                  </div>
                  <div
                    class="min-h-6 break-words text-lf-text-muted line-through decoration-lf-border"
                  >
                    <SegmentTextDisplay :text="item.before" :mode="textRenderMode" />
                  </div>
                </div>
                <div class="min-w-0">
                  <div
                    class="mb-0.5 text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase"
                  >
                    {{ t('workspace.segment.searchReplace.after') }}
                  </div>
                  <div class="min-h-6 break-words font-medium text-lf-text-strong">
                    <SegmentTextDisplay :text="item.after" :mode="textRenderMode" />
                  </div>
                </div>
              </div>
            </div>
            <p
              v-if="preview.matched_segment_count > preview.items.length"
              class="text-xs text-lf-text-subtle"
            >
              {{
                t('workspace.segment.searchReplace.samplesTruncated', {
                  shown: preview.items.length,
                })
              }}
            </p>
          </section>
        </template>

        <!-- 应用结果 -->
        <section
          v-if="lastApplyResult"
          class="space-y-2.5 rounded-xl border border-lf-border-soft bg-lf-surface-muted/50 px-3.5 py-3"
        >
          <div class="flex flex-wrap items-center gap-2 text-sm">
            <span class="font-semibold text-lf-text-strong">
              {{ t('workspace.segment.searchReplace.applyResultTitle') }}
            </span>
            <NTag size="small" type="success" :bordered="false">
              {{
                t('workspace.segment.searchReplace.appliedCount', {
                  count: lastApplyResult.applied_count,
                })
              }}
            </NTag>
            <NTag
              v-if="lastApplyResult.skipped_count > 0"
              size="small"
              type="warning"
              :bordered="false"
            >
              {{
                t('workspace.segment.searchReplace.skippedCount', {
                  count: lastApplyResult.skipped_count,
                })
              }}
            </NTag>
          </div>
          <div v-if="lastApplyResult.skipped?.length" class="space-y-1">
            <div
              v-for="item in lastApplyResult.skipped"
              :key="item.segment_id"
              class="text-xs text-lf-text-muted"
            >
              #{{ item.segment_id }} · {{ skippedReasonLabel(item.reason) }}
            </div>
          </div>
        </section>

        <NEmpty
          v-if="!preview && !lastApplyResult && !errorMessage"
          :description="t('workspace.segment.searchReplace.empty')"
          class="py-8"
        />
      </div>

      <template #footer>
        <div class="flex items-center justify-between gap-3">
          <NButton
            tertiary
            size="small"
            :loading="undoing"
            :disabled="busy || !undoableOperationId"
            @click="handleUndo"
          >
            {{ t('workspace.segment.searchReplace.undo') }}
          </NButton>
          <NButton quaternary size="small" :disabled="busy" @click="close">
            {{ t('workspace.common.close') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
