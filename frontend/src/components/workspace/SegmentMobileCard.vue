<script setup lang="ts">
import { NButton, NIcon, NInput, NPopover, NTag, NText } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import IconCarbonChat from '~icons/carbon/chat'
import IconCarbonUndo from '~icons/carbon/undo'
import IconCarbonTrashCan from '~icons/carbon/trash-can'

import type { ApiSchemas } from '@/api/client'
import type { SegmentFormModel } from '@/composables/useSegmentEditing'
import {
  formatQualityIssueTooltip,
  getQualityCodeLabel,
  hasPendingSemanticIssues,
  isIssueDismissed,
  type QualityIssue,
} from '@/composables/useQualityIssues'
import {
  renderSearchHighlightedHtml,
  renderSearchHighlightedText,
  type SearchMatchMode,
  type SearchMatchOptions,
} from '@/composables/useSearchHighlight'
import { formatDate, getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import SegmentTextDisplay from '@/components/workspace/SegmentTextDisplay.vue'

type Segment = ApiSchemas['Segment']

const { t } = useI18n()

const props = withDefaults(
  defineProps<{
    segment: Segment
    textRenderMode: 'plaintext' | 'html'
    showUpdatedAt: boolean
    showComment: boolean
    isEditing: boolean
    editForm: SegmentFormModel
    isSaving: boolean
    isCommentVisible: boolean
    commentText: string
    /** 搜索定位面板关键词（激活时源文/译文正文以搜索高亮渲染，替代质量标记） */
    searchQuery?: string
    /** 搜索字段范围：被排除的字段不做搜索高亮，走普通文本 / 质量标记路径 */
    searchField?: 'source' | 'target' | 'both'
    searchCaseSensitive?: boolean
    /** 搜索定位匹配模式（substring / regex），与桌面表格同源 */
    searchMatchMode?: SearchMatchMode
    /** 搜索定位全字匹配，与桌面表格同源 */
    searchWholeWord?: boolean
  }>(),
  {
    searchQuery: '',
    searchField: 'both',
    searchCaseSensitive: true,
    searchMatchMode: 'substring',
    searchWholeWord: false,
  },
)

const activeIssueIndex = ref<number | null>(null)

const toggleIssueHighlight = (issueIndex: number): void => {
  if (props.textRenderMode !== 'html') return
  activeIssueIndex.value = activeIssueIndex.value === issueIndex ? null : issueIndex
}

// ── 搜索高亮（与桌面表格同一套 options 与渲染函数）──
const searchMatchOptions = computed<SearchMatchOptions>(() => ({
  caseSensitive: props.searchCaseSensitive,
  wholeWord: props.searchWholeWord,
  matchMode: props.searchMatchMode,
}))

const activeSearchQuery = computed(() => props.searchQuery.trim())
const sourceSearched = computed(
  () => Boolean(activeSearchQuery.value) && props.searchField !== 'target',
)
const targetSearched = computed(
  () => Boolean(activeSearchQuery.value) && props.searchField !== 'source',
)

/** 源文正文：搜索激活时走命中高亮，否则保持普通文本 / 质量标记路径 */
const sourceBody = computed(() =>
  sourceSearched.value
    ? props.textRenderMode === 'html'
      ? renderSearchHighlightedHtml(
          props.segment.source_text,
          activeSearchQuery.value,
          searchMatchOptions.value,
        )
      : renderSearchHighlightedText(
          props.segment.source_text,
          activeSearchQuery.value,
          searchMatchOptions.value,
        )
    : h(SegmentTextDisplay, { text: props.segment.source_text, mode: props.textRenderMode }),
)

/** 译文正文：搜索激活时走命中高亮，否则保留质量问题标记 */
const targetBody = computed(() => {
  const targetText = props.segment.target_text ?? ''
  if (!targetSearched.value) {
    return h(SegmentTextDisplay, {
      text: targetText,
      issues: props.segment.quality_issues,
      mode: props.textRenderMode,
      activeIssueIndex: activeIssueIndex.value,
    })
  }
  return props.textRenderMode === 'html'
    ? renderSearchHighlightedHtml(targetText, activeSearchQuery.value, searchMatchOptions.value)
    : renderSearchHighlightedText(targetText, activeSearchQuery.value, searchMatchOptions.value)
})

const emit = defineEmits<{
  startEdit: [segment: Segment]
  cancelEdit: []
  saveEdit: [segment: Segment]
  saveAndNext: [segment: Segment]
  openComment: [segment: Segment]
  saveComment: [segment: Segment]
  closeComment: []
  updateEditField: [field: 'target_text' | 'comment', value: string]
  updateCommentText: [value: string]
  previewTranslation: [segment: Segment]
  previewRevision: [segment: Segment]
  dismissIssue: [segment: Segment, issue: QualityIssue]
  reinstateIssue: [segment: Segment, issue: QualityIssue]
}>()
</script>

<template>
  <div class="space-y-2 rounded-lf-card border border-lf-border-soft bg-lf-surface p-3">
    <!-- 序号与状态 -->
    <div class="flex items-center justify-between">
      <span class="text-xs text-lf-text-muted">#{{ segment.segment_index }}</span>
      <div class="flex items-center gap-1">
        <template v-if="segment.quality_issues?.length">
          <NPopover
            v-for="(issue, issueIndex) in segment.quality_issues"
            :key="`${issue.code}-${issue.span?.matched_text ?? issueIndex}`"
            trigger="click"
            placement="bottom"
            :style="{ maxWidth: '320px' }"
          >
            <template #trigger>
              <NTag
                size="small"
                :type="
                  isIssueDismissed(issue)
                    ? 'default'
                    : issue.severity === 'error'
                      ? 'error'
                      : 'warning'
                "
                round
                :class="[
                  isIssueDismissed(issue) ? 'line-through opacity-60' : '',
                  textRenderMode === 'html' ? 'cursor-pointer' : '',
                ]"
                @click="toggleIssueHighlight(issueIndex)"
              >
                {{ getQualityCodeLabel(issue.code) }}
              </NTag>
            </template>
            <div class="space-y-2">
              <div class="whitespace-pre-line text-xs leading-relaxed">
                {{ formatQualityIssueTooltip(issue) }}
              </div>
              <NButton
                size="tiny"
                quaternary
                :type="isIssueDismissed(issue) ? 'default' : 'warning'"
                @click="
                  isIssueDismissed(issue)
                    ? emit('reinstateIssue', segment, issue)
                    : emit('dismissIssue', segment, issue)
                "
              >
                <template #icon>
                  <NIcon
                    :size="12"
                    :component="isIssueDismissed(issue) ? IconCarbonUndo : IconCarbonTrashCan"
                  />
                </template>
                {{
                  isIssueDismissed(issue)
                    ? t('workspace.segment.disposition.reinstateAction')
                    : t('workspace.segment.disposition.dismissAction')
                }}
              </NButton>
            </div>
          </NPopover>
        </template>
        <NTag size="small" :type="statusTagType(segment.status)">
          {{ getSegmentStatusLabel(segment.status) }}
        </NTag>
      </div>
    </div>

    <!-- 源文本 -->
    <div>
      <p class="mb-1 text-xs text-lf-text-muted">{{ t('workspace.segment.columns.source') }}</p>
      <!-- data-search-field：与桌面列同名容器，锚点跳转据此在命中字段正文内定位 mark -->
      <div data-search-field="source">
        <component :is="sourceBody" />
      </div>
    </div>

    <!-- 译文 -->
    <div>
      <p class="mb-1 text-xs text-lf-text-muted">{{ t('workspace.segment.columns.target') }}</p>
      <div v-if="isEditing">
        <NInput
          :value="editForm.target_text"
          type="textarea"
          :autosize="{ minRows: 2, maxRows: 6 }"
          :placeholder="t('workspace.segment.form.target')"
          @update:value="(val: string) => emit('updateEditField', 'target_text', val)"
        />
      </div>
      <template v-else>
        <!-- data-search-field：与桌面列同名容器；命中高亮 / 质量标记两条路径共用 -->
        <div v-if="segment.target_text" data-search-field="target">
          <component :is="targetBody" />
        </div>
        <div
          v-else
          class="flex min-h-10 items-center justify-center rounded-lf-ctl border border-dashed border-lf-border-soft bg-lf-info-soft px-3 py-2"
        >
          <NText depth="3">{{ t('workspace.segment.emptyTarget') }}</NText>
        </div>
      </template>
    </div>

    <!-- 更新时间 -->
    <p v-if="showUpdatedAt" class="text-xs text-lf-text-muted">
      {{ formatDate(segment.updated_at) }}
    </p>

    <!-- 评论摘要（有评论时显示） -->
    <div
      v-if="showComment && segment.review_comment && !isCommentVisible"
      class="flex items-center gap-1 text-xs text-lf-text-muted"
    >
      <NIcon :size="14" :component="IconCarbonChat" />
      <span class="truncate">{{ segment.review_comment }}</span>
    </div>

    <!-- 评论编辑区（行内展开） -->
    <div
      v-if="showComment && isCommentVisible"
      class="rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted p-3"
    >
      <p class="mb-2 text-xs text-lf-text-muted">{{ t('workspace.segment.form.comment') }}</p>
      <NInput
        :value="commentText"
        type="textarea"
        :autosize="{ minRows: 2, maxRows: 4 }"
        :placeholder="t('workspace.segment.form.comment')"
        @update:value="(val: string) => emit('updateCommentText', val)"
      />
      <div class="mt-2 flex justify-end gap-2">
        <NButton size="tiny" @click="emit('closeComment')">
          {{ t('workspace.segment.actions.cancelInline') }}
        </NButton>
        <NButton size="tiny" type="primary" @click="emit('saveComment', segment)">
          {{ t('common.save') }}
        </NButton>
      </div>
    </div>

    <!-- 编辑态评论 -->
    <div v-if="isEditing" class="pt-1">
      <NInput
        :value="editForm.comment"
        type="textarea"
        :autosize="{ minRows: 1, maxRows: 3 }"
        :placeholder="t('workspace.segment.form.comment')"
        @update:value="(val: string) => emit('updateEditField', 'comment', val)"
      />
    </div>

    <!-- 操作按钮 -->
    <div class="flex items-center justify-end gap-2 pt-1">
      <template v-if="isEditing">
        <NButton size="tiny" quaternary @click="emit('cancelEdit')">
          {{ t('workspace.segment.actions.cancelInline') }}
        </NButton>
        <NButton size="tiny" type="primary" :loading="isSaving" @click="emit('saveEdit', segment)">
          {{ t('workspace.segment.actions.saveInline') }}
        </NButton>
        <NButton
          size="tiny"
          type="primary"
          :loading="isSaving"
          @click="emit('saveAndNext', segment)"
        >
          {{ t('workspace.segment.actions.saveAndNext') }}
        </NButton>
      </template>
      <template v-else>
        <!-- 评论按钮 -->
        <NButton v-if="showComment" size="tiny" quaternary @click="emit('openComment', segment)">
          {{ t('workspace.segment.actions.comment') }}
        </NButton>

        <!-- 编辑按钮 -->
        <NButton
          size="tiny"
          secondary
          type="primary"
          :loading="isSaving"
          @click="emit('startEdit', segment)"
        >
          {{ t('workspace.segment.actions.edit') }}
        </NButton>

        <!-- 翻译按钮 -->
        <NButton size="tiny" type="primary" @click="emit('previewTranslation', segment)">
          {{ t('workspace.segment.actions.previewTranslation') }}
        </NButton>

        <!-- 修订按钮 -->
        <NButton
          v-if="
            (segment.status === 'translated' || segment.status === 'edited') &&
            segment.target_text &&
            hasPendingSemanticIssues(segment)
          "
          size="tiny"
          secondary
          type="primary"
          @click="emit('previewRevision', segment)"
        >
          {{ t('workspace.segment.actions.previewRevision') }}
        </NButton>
      </template>
    </div>
  </div>
</template>
