<script setup lang="ts">
import { NButton, NIcon, NInput, NTag, NText, NTooltip } from 'naive-ui'

import IconCarbonChat from '~icons/carbon/chat'

import type { ApiSchemas } from '@/api/client'
import type { SegmentFormModel } from '@/composables/useSegmentEditing'
import {
  formatQualityIssueTooltip,
  getQualityCodeLabel,
  renderQualityHighlightedHtml,
} from '@/composables/useQualityIssues'
import { formatDate, getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import HtmlContent from '@/components/workspace/HtmlContent.vue'
import QualityHighlightedText from '@/components/workspace/QualityHighlightedText.vue'
import { t } from '@/i18n'

type Segment = ApiSchemas['Segment']

const props = defineProps<{
  segment: Segment
  textRenderMode: 'plaintext' | 'html'
  showUpdatedAt: boolean
  showComment: boolean
  isEditing: boolean
  editForm: SegmentFormModel
  isSaving: boolean
  isCommentVisible: boolean
  commentText: string
}>()

const activeIssueIndex = ref<number | null>(null)

const toggleIssueHighlight = (issueIndex: number): void => {
  if (props.textRenderMode !== 'html') return
  activeIssueIndex.value = activeIssueIndex.value === issueIndex ? null : issueIndex
}

const highlightedTargetVNode = computed(() => {
  if (props.textRenderMode !== 'html') return null
  if (!props.segment.target_text || !props.segment.quality_issues?.length) return null
  return renderQualityHighlightedHtml(
    props.segment.target_text,
    props.segment.quality_issues,
    activeIssueIndex.value,
    4,
  )
})

const emit = defineEmits<{
  startEdit: [segment: Segment]
  cancelEdit: []
  saveEdit: [segment: Segment]
  saveAndNext: [segment: Segment]
  openComment: [segment: Segment]
  saveComment: [segment: Segment]
  closeComment: []
  updateEditField: [field: 'source_text' | 'target_text' | 'comment', value: string]
  updateCommentText: [value: string]
  translate: [segment: Segment]
}>()
</script>

<template>
  <div class="space-y-2 rounded-xl border border-lf-border-soft bg-lf-surface p-3">
    <!-- 序号与状态 -->
    <div class="flex items-center justify-between">
      <span class="text-xs text-lf-text-muted">#{{ segment.segment_index }}</span>
      <div class="flex items-center gap-1">
        <template v-if="segment.quality_issues?.length">
          <NTooltip
            v-for="(issue, issueIndex) in segment.quality_issues"
            :key="`${issue.code}-${issue.span?.matched_text ?? issueIndex}`"
          >
            <template #trigger>
              <NTag
                size="small"
                :type="issue.severity === 'error' ? 'error' : 'warning'"
                round
                :class="{ 'cursor-pointer': textRenderMode === 'html' }"
                @click="toggleIssueHighlight(issueIndex)"
              >
                {{ getQualityCodeLabel(issue.code) }}
              </NTag>
            </template>
            <div class="whitespace-pre-line text-xs leading-relaxed">
              {{ formatQualityIssueTooltip(issue) }}
            </div>
          </NTooltip>
        </template>
        <NTag size="small" :type="statusTagType(segment.status)">
          {{ getSegmentStatusLabel(segment.status) }}
        </NTag>
      </div>
    </div>

    <!-- 源文本 -->
    <div>
      <p class="mb-1 text-xs text-lf-text-muted">{{ t('workspace.segment.columns.source') }}</p>
      <HtmlContent v-if="textRenderMode === 'html'" :content="segment.source_text" :max-lines="4" />
      <span v-else>{{ segment.source_text }}</span>
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
        <component :is="highlightedTargetVNode" v-if="highlightedTargetVNode" />
        <HtmlContent
          v-else-if="segment.target_text && textRenderMode === 'html'"
          :content="segment.target_text"
          :max-lines="4"
        />
        <QualityHighlightedText
          v-else-if="segment.target_text"
          :text="segment.target_text"
          :issues="segment.quality_issues"
        />
        <div v-else class="target-empty">
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
      class="rounded-lg border border-lf-border-soft bg-lf-surface-muted p-3"
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
          {{ t('workspace.common.save') }}
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
        <NButton size="tiny" type="primary" @click="emit('translate', segment)">
          {{ t('workspace.segment.actions.translate') }}
        </NButton>
      </template>
    </div>
  </div>
</template>
