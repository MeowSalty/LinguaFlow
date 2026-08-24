<script setup lang="ts">
import { NButton, NIcon, NInput, NPopover, NTag, NText } from 'naive-ui'

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
import { formatDate, getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import SegmentTextDisplay from '@/components/workspace/SegmentTextDisplay.vue'
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
  <div class="space-y-2 rounded-xl border border-lf-border-soft bg-lf-surface p-3">
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
      <SegmentTextDisplay :text="segment.source_text" :mode="textRenderMode" />
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
        <SegmentTextDisplay
          v-if="segment.target_text"
          :text="segment.target_text"
          :issues="segment.quality_issues"
          :mode="textRenderMode"
          :active-issue-index="activeIssueIndex"
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
