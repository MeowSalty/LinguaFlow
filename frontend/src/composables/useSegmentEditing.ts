import { computed, onScopeDispose, reactive, ref, watch, type Ref } from 'vue'
import type { SelectOption } from 'naive-ui'
import { NInput, useDialog, useMessage } from 'naive-ui'
import { h } from 'vue'

import { type ApiSchemas } from '@/api/client'
import { formatQualityIssueTooltip, type QualityIssue } from '@/composables/useQualityIssues'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { useWorkspaceDraftGuard, type DraftSaveResult } from './useWorkspaceDraftGuard'
import { registerWorkspaceDraftReader } from '@/utils/workspace-draft-state'

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
  let editBaseline: SegmentFormModel = { target_text: '', comment: '' }
  let commentBaseline = ''
  let editRevision = 0
  let commentRevision = 0
  let contextRevision = 0
  let selectionRevision = 0
  let disposed = false
  let pendingSave: Promise<DraftSaveResult> | null = null
  const captureEditingContext = () => {
    const session = captureSession()
    const project = projectId.value
    const resource = activeResourceId.value
    const revision = contextRevision
    return () =>
      !disposed &&
      revision === contextRevision &&
      isSessionCurrent(session) &&
      projectId.value === project &&
      activeResourceId.value === resource
  }
  const hasPendingDrafts = (): boolean =>
    (inlineEditingSegmentId.value !== null &&
      (inlineEditForm.target_text !== editBaseline.target_text ||
        inlineEditForm.comment !== editBaseline.comment)) ||
    (inlineCommentVisible.value !== null && inlineCommentText.value !== commentBaseline)
  const discardPendingDrafts = (): void => {
    editRevision++
    commentRevision++
    inlineEditingSegmentId.value = null
    inlineCommentVisible.value = null
    inlineEditForm.target_text = ''
    inlineEditForm.comment = ''
    inlineCommentText.value = ''
  }
  onScopeDispose(
    registerWorkspaceDraftReader({
      projectId: () => projectId.value,
      resourceId: () => activeResourceId.value,
      pending: hasPendingDrafts,
    }),
  )
  const persistDrafts = async (): Promise<DraftSaveResult> => {
    if (disposed) return 'stale'
    if (!hasPendingDrafts()) {
      discardPendingDrafts()
      return 'saved'
    }
    const project = projectId.value
    const resource = activeResourceId.value
    if (!project || !resource) return 'stale'
    const current = captureEditingContext()
    const editId = inlineEditingSegmentId.value
    const commentId = inlineCommentVisible.value
    const editVersion = editRevision
    const commentVersion = commentRevision
    const form = { ...inlineEditForm }
    const note = inlineCommentText.value
    try {
      if (
        editId !== null &&
        (form.target_text !== editBaseline.target_text || form.comment !== editBaseline.comment)
      ) {
        await workspace.updateSegment(project, resource, editId, {
          ...(form.target_text !== editBaseline.target_text
            ? { target_text: form.target_text }
            : {}),
          ...(form.comment !== editBaseline.comment ? { comment: form.comment } : {}),
        })
        if (!current() || editVersion !== editRevision) return 'stale'
        editBaseline = form
      }
      if (commentId !== null && note !== commentBaseline) {
        await workspace.updateSegment(project, resource, commentId, { comment: note })
        if (!current() || commentVersion !== commentRevision) return 'stale'
        commentBaseline = note
      }
      if (!current()) return 'stale'
      if (hasPendingDrafts()) {
        message.warning(t('sourceStorage.drafts.changedDuringSave'))
        return 'failed'
      }
      discardPendingDrafts()
      message.success(t('workspace.messages.segmentSaved'))
      return 'saved'
    } catch {
      if (!current()) return 'stale'
      message.error(workspace.actionError || t('workspace.messages.segmentSaveFailed'))
      return 'failed'
    }
  }
  const savePendingDrafts = (): Promise<DraftSaveResult> => {
    if (!pendingSave) {
      const saving = persistDrafts().finally(() => {
        if (pendingSave === saving) pendingSave = null
      })
      pendingSave = saving
    }
    return pendingSave
  }
  const { confirmPendingDrafts } = useWorkspaceDraftGuard(() => ({
    hasPendingDrafts,
    savePendingDrafts,
    discardPendingDrafts,
  }))
  const invalidateEditingContext = (): void => {
    contextRevision++
    selectionRevision++
    pendingSave = null
    discardPendingDrafts()
  }
  onScopeDispose(onSessionChange(invalidateEditingContext))
  onScopeDispose(() => {
    disposed = true
    invalidateEditingContext()
  })
  watch([projectId, activeResourceId], invalidateEditingContext, { flush: 'sync' })

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
  const startInlineEdit = async (segment: Segment): Promise<void> => {
    if (inlineEditingSegmentId.value === segment.id) return
    const current = captureEditingContext()
    const selection = ++selectionRevision
    if (!(await confirmPendingDrafts()) || !current() || selection !== selectionRevision) return
    discardPendingDrafts()
    inlineEditingSegmentId.value = segment.id
    inlineEditForm.target_text = segment.target_text ?? ''
    inlineEditForm.comment = segment.review_comment ?? ''
    editBaseline = { ...inlineEditForm }
  }

  const cancelInlineEdit = (): void => {
    editRevision++
    inlineEditingSegmentId.value = null
    inlineEditForm.target_text = ''
    inlineEditForm.comment = ''
  }

  const saveInlineEdit = async (_segment: Segment): Promise<void> => {
    await savePendingDrafts()
  }

  const saveAndEditNext = async (segment: Segment, segments: Segment[]): Promise<void> => {
    const current = captureEditingContext()
    const selection = selectionRevision
    if ((await savePendingDrafts()) === 'saved' && current() && selection === selectionRevision) {
      const idx = segments.findIndex((s) => s.id === segment.id)
      const nextSegment = idx >= 0 ? segments[idx + 1] : undefined
      if (nextSegment) {
        await startInlineEdit(nextSegment)
      } else {
        cancelInlineEdit()
      }
    }
  }

  const openInlineComment = async (segment: Segment): Promise<void> => {
    if (inlineCommentVisible.value === segment.id) return
    const current = captureEditingContext()
    const selection = ++selectionRevision
    if (!(await confirmPendingDrafts()) || !current() || selection !== selectionRevision) return
    discardPendingDrafts()
    inlineCommentVisible.value = segment.id
    inlineCommentText.value = segment.review_comment ?? ''
    commentBaseline = inlineCommentText.value
  }

  const saveInlineComment = async (_segment: Segment): Promise<void> => {
    await savePendingDrafts()
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
    hasPendingDrafts,
    savePendingDrafts,
    discardPendingDrafts,
    confirmPendingDrafts,
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
