import { computed, reactive, ref, type Ref } from 'vue'
import type { SelectOption } from 'naive-ui'
import { useMessage } from 'naive-ui'

import { type ApiSchemas } from '@/api/client'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'

export { formatDate, getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'

type Segment = ApiSchemas['Segment']

export interface SegmentFormModel {
  source_text: string
  target_text: string
  comment: string
}

export function useSegmentEditing(
  projectId: Ref<number | null>,
  activeResourceId: Ref<number | null>,
) {
  const message = useMessage()
  const workspace = useProjectWorkspaceStore()

  // ── 内联编辑状态 ──
  const inlineEditingSegmentId = ref<number | null>(null)
  const inlineEditForm = reactive<SegmentFormModel>({
    source_text: '',
    target_text: '',
    comment: '',
  })
  const inlineCommentVisible = ref<number | null>(null)
  const inlineCommentText = ref('')

  // ── 过滤选项 ──
  const segmentStatusOptions = computed<SelectOption[]>(() => [
    { label: t('workspace.filters.allStatuses'), value: 'all' },
    { label: t('workspace.segment.status.pending'), value: 'pending' },
    { label: t('workspace.segment.status.translated'), value: 'translated' },
    { label: t('workspace.segment.status.edited'), value: 'edited' },
    { label: t('workspace.segment.status.approved'), value: 'approved' },
    { label: t('workspace.segment.status.rejected'), value: 'rejected' },
  ])

  // ── 方法 ──
  const startInlineEdit = (segment: Segment): void => {
    inlineEditingSegmentId.value = segment.id
    inlineEditForm.source_text = segment.source_text
    inlineEditForm.target_text = segment.target_text ?? ''
    inlineEditForm.comment = segment.review_comment ?? ''
  }

  const cancelInlineEdit = (): void => {
    inlineEditingSegmentId.value = null
    inlineEditForm.source_text = ''
    inlineEditForm.target_text = ''
    inlineEditForm.comment = ''
  }

  const saveInlineEdit = async (segment: Segment): Promise<void> => {
    if (!projectId.value || !activeResourceId.value) {
      return
    }

    try {
      await workspace.updateSegment(projectId.value, activeResourceId.value, segment.id, {
        source_text: inlineEditForm.source_text,
        target_text: inlineEditForm.target_text || undefined,
        comment: inlineEditForm.comment || undefined,
      })
      message.success(t('workspace.messages.segmentSaved'))
      cancelInlineEdit()
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.segmentSaveFailed'))
    }
  }

  const openInlineComment = (segment: Segment): void => {
    inlineCommentVisible.value = segment.id
    inlineCommentText.value = segment.review_comment ?? ''
  }

  const saveInlineComment = async (segment: Segment): Promise<void> => {
    if (!projectId.value || !activeResourceId.value) {
      return
    }

    try {
      await workspace.updateSegment(projectId.value, activeResourceId.value, segment.id, {
        comment: inlineCommentText.value || undefined,
      })
      inlineCommentVisible.value = null
      message.success(t('workspace.messages.segmentSaved'))
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.segmentSaveFailed'))
    }
  }

  return {
    // 状态
    inlineEditingSegmentId,
    inlineEditForm,
    inlineCommentVisible,
    inlineCommentText,
    // 计算属性
    segmentStatusOptions,
    // 方法
    startInlineEdit,
    cancelInlineEdit,
    saveInlineEdit,
    openInlineComment,
    saveInlineComment,
  }
}
