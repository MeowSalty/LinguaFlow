import { computed, reactive, ref, type Ref } from 'vue'
import type { SelectOption } from 'naive-ui'
import { NInput, useDialog, useMessage } from 'naive-ui'
import { h } from 'vue'

import { type ApiSchemas } from '@/api/client'
import { formatQualityIssueTooltip, type QualityIssue } from '@/composables/useQualityIssues'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'

export { formatDate, getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'

type Segment = ApiSchemas['Segment']

export interface SegmentFormModel {
  target_text: string
  comment: string
}

export function useSegmentEditing(
  projectId: Ref<number | null>,
  activeResourceId: Ref<number | null>,
) {
  const message = useMessage()
  const dialog = useDialog()
  const workspace = useProjectWorkspaceStore()

  // ── 内联编辑状态 ──
  const inlineEditingSegmentId = ref<number | null>(null)
  const inlineEditForm = reactive<SegmentFormModel>({
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
    inlineEditForm.target_text = segment.target_text ?? ''
    inlineEditForm.comment = segment.review_comment ?? ''
  }

  const cancelInlineEdit = (): void => {
    inlineEditingSegmentId.value = null
    inlineEditForm.target_text = ''
    inlineEditForm.comment = ''
  }

  const saveInlineEdit = async (segment: Segment): Promise<void> => {
    if (!projectId.value || !activeResourceId.value) {
      return
    }

    try {
      await workspace.updateSegment(projectId.value, activeResourceId.value, segment.id, {
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

  const saveAndEditNext = async (segment: Segment, segments: Segment[]): Promise<void> => {
    if (!projectId.value || !activeResourceId.value) {
      return
    }

    try {
      await workspace.updateSegment(projectId.value, activeResourceId.value, segment.id, {
        target_text: inlineEditForm.target_text || undefined,
        comment: inlineEditForm.comment || undefined,
      })
      message.success(t('workspace.messages.segmentSaved'))

      const idx = segments.findIndex((s) => s.id === segment.id)
      const nextSegment = idx >= 0 ? segments[idx + 1] : undefined
      if (nextSegment) {
        startInlineEdit(nextSegment)
      } else {
        cancelInlineEdit()
      }
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

  // ── 质量问题裁决 ──

  const submitDisposition = async (
    segment: Segment,
    issue: QualityIssue,
    disposition: 'pending' | 'dismissed',
    note: string,
  ): Promise<void> => {
    if (!projectId.value || !activeResourceId.value) return
    try {
      await workspace.setIssueDisposition(projectId.value, activeResourceId.value, segment.id, {
        code: issue.code,
        matched_text: issue.span?.matched_text ?? '',
        disposition,
        note: note || undefined,
      })
      message.success(
        disposition === 'dismissed'
          ? t('workspace.segment.disposition.dismissSuccess')
          : t('workspace.segment.disposition.reinstateSuccess'),
      )
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('api.errors.setIssueDispositionFailed'))
    }
  }

  /** 弹出对话框确认驳回（含可选 note 输入） */
  const dismissIssue = (segment: Segment, issue: QualityIssue): void => {
    let note = ''
    dialog.info({
      title: t('workspace.segment.disposition.dismissTitle'),
      content: () =>
        h('div', { class: 'space-y-3' }, [
          h(
            'div',
            { class: 'whitespace-pre-line text-sm leading-relaxed text-lf-text-muted' },
            formatQualityIssueTooltip(issue),
          ),
          h(NInput, {
            type: 'textarea',
            autosize: { minRows: 2, maxRows: 4 },
            placeholder: t('workspace.segment.disposition.notePlaceholder'),
            'onUpdate:value': (val: string) => {
              note = val
            },
          }),
        ]),
      positiveText: t('workspace.segment.disposition.confirmDismiss'),
      negativeText: t('workspace.segment.disposition.cancel'),
      onPositiveClick: () => {
        void submitDisposition(segment, issue, 'dismissed', note)
      },
    })
  }

  /** 弹出对话框确认撤销裁决 */
  const reinstateIssue = (segment: Segment, issue: QualityIssue): void => {
    dialog.warning({
      title: t('workspace.segment.disposition.reinstateTitle'),
      content: formatQualityIssueTooltip(issue),
      positiveText: t('workspace.segment.disposition.confirmReinstate'),
      negativeText: t('workspace.segment.disposition.cancel'),
      onPositiveClick: () => {
        void submitDisposition(segment, issue, 'pending', '')
      },
    })
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
    saveAndEditNext,
    openInlineComment,
    saveInlineComment,
    dismissIssue,
    reinstateIssue,
  }
}
