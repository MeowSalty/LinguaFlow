import { computed, h, reactive, ref, type Ref } from 'vue'
import type { DataTableColumns, SelectOption } from 'naive-ui'
import { NButton, NInput, NPopover, NSpace, NTag, NText, useMessage } from 'naive-ui'

import { type ApiSchemas } from '@/api/client'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'
import { formatDate, getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'

type Segment = ApiSchemas['Segment']

export interface SegmentFormModel {
  source_text: string
  target_text: string
  comment: string
}

export function useSegmentEditing(
  projectId: Ref<number | null>,
  activeResourceId: Ref<number | null>,
  onTranslate?: (segment: Segment) => void,
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
    { label: t('workspace.segment.status.reviewed'), value: 'reviewed' },
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
        source_text: segment.source_text,
        target_text: segment.target_text || undefined,
        comment: inlineCommentText.value || undefined,
      })
      inlineCommentVisible.value = null
      message.success(t('workspace.messages.segmentSaved'))
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.segmentSaveFailed'))
    }
  }

  // ── 表格列定义 ──
  const segmentColumns = computed<DataTableColumns<Segment>>(() => [
    {
      title: '#',
      key: 'segment_index',
      width: 76,
    },
    {
      title: t('workspace.segment.columns.source'),
      key: 'source_text',
      minWidth: 260,
      render: (row) => {
        if (inlineEditingSegmentId.value === row.id) {
          return h(NInput, {
            value: inlineEditForm.source_text,
            type: 'textarea',
            autosize: { minRows: 2, maxRows: 6 },
            'onUpdate:value': (val: string) => {
              inlineEditForm.source_text = val
            },
          })
        }
        return row.source_text
      },
    },
    {
      title: t('workspace.segment.columns.target'),
      key: 'target_text',
      minWidth: 260,
      render: (row) => {
        if (inlineEditingSegmentId.value === row.id) {
          return h(NInput, {
            value: inlineEditForm.target_text,
            type: 'textarea',
            autosize: { minRows: 2, maxRows: 6 },
            placeholder: t('workspace.segment.form.target'),
            'onUpdate:value': (val: string) => {
              inlineEditForm.target_text = val
            },
          })
        }
        return (
          row.target_text ||
          h(NText, { depth: 3 }, { default: () => t('workspace.segment.emptyTarget') })
        )
      },
    },
    {
      title: t('workspace.segment.columns.status'),
      key: 'status',
      width: 120,
      render: (row) =>
        h(
          NTag,
          { size: 'small', type: statusTagType(row.status) },
          { default: () => getSegmentStatusLabel(row.status) },
        ),
    },
    {
      title: t('workspace.common.updatedAt'),
      key: 'updated_at',
      width: 170,
      render: (row) => formatDate(row.updated_at),
    },
    {
      title: t('workspace.common.actions'),
      key: 'actions',
      width: 220,
      fixed: 'right',
      render: (row) => {
        if (inlineEditingSegmentId.value === row.id) {
          return h(NSpace, { size: 4, wrap: false }, () => [
            h(
              NButton,
              {
                size: 'small',
                quaternary: true,
                onClick: () => cancelInlineEdit(),
              },
              { default: () => t('workspace.segment.actions.cancelInline') },
            ),
            h(
              NButton,
              {
                size: 'small',
                type: 'primary',
                loading: workspace.editingSegmentIds.includes(row.id),
                onClick: () => saveInlineEdit(row),
              },
              { default: () => t('workspace.segment.actions.saveInline') },
            ),
          ])
        }
        return h(NSpace, { size: 4, wrap: false }, () => [
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              type: 'primary',
              loading: workspace.editingSegmentIds.includes(row.id),
              onClick: () => startInlineEdit(row),
            },
            { default: () => t('workspace.segment.actions.edit') },
          ),
          h(
            NPopover,
            {
              show: inlineCommentVisible.value === row.id,
              trigger: 'click',
              placement: 'bottom',
              'onUpdate:show': (show: boolean) => {
                if (show) {
                  openInlineComment(row)
                } else {
                  inlineCommentVisible.value = null
                }
              },
            },
            {
              trigger: () =>
                h(
                  NButton,
                  {
                    size: 'small',
                    quaternary: true,
                  },
                  { default: () => t('workspace.segment.actions.comment') },
                ),
              default: () =>
                h('div', { class: 'w-64 space-y-3' }, [
                  h(NInput, {
                    value: inlineCommentText.value,
                    type: 'textarea',
                    autosize: { minRows: 2, maxRows: 4 },
                    placeholder: t('workspace.segment.form.comment'),
                    'onUpdate:value': (val: string) => {
                      inlineCommentText.value = val
                    },
                  }),
                  h(
                    NButton,
                    {
                      size: 'small',
                      type: 'primary',
                      block: true,
                      onClick: () => saveInlineComment(row),
                    },
                    { default: () => t('workspace.common.save') },
                  ),
                ]),
            },
          ),
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              onClick: () => onTranslate?.(row),
            },
            { default: () => t('workspace.segment.actions.translate') },
          ),
        ])
      },
    },
  ])

  return {
    // 状态
    inlineEditingSegmentId,
    inlineEditForm,
    inlineCommentVisible,
    inlineCommentText,
    // 计算属性
    segmentStatusOptions,
    segmentColumns,
    // 方法
    startInlineEdit,
    cancelInlineEdit,
    saveInlineEdit,
    openInlineComment,
    saveInlineComment,
  }
}
