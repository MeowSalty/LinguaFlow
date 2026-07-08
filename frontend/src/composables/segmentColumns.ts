import type { DataTableColumns } from 'naive-ui'
import { NButton, NInput, NSpace, NTag, NText } from 'naive-ui'
import type { ComputedRef, Ref } from 'vue'
import { computed, h } from 'vue'

import type { ApiSchemas } from '@/api/client'
import type { SegmentFormModel } from '@/composables/useSegmentEditing'
import { formatDate, getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import HtmlContent from '@/components/workspace/HtmlContent.vue'
import { t } from '@/i18n'

type Segment = ApiSchemas['Segment']

/**
 * 段落表格配置项
 * 控制列定义中的条件渲染行为
 */
export interface SegmentTableConfig {
  /** 文本渲染模式 */
  textRenderMode: 'plaintext' | 'html'
  /** 是否显示 updated_at 列 */
  showUpdatedAt: boolean
  /** 是否显示评论功能 */
  showComment: boolean
  /** 是否显示行选择框 */
  showSelection: boolean
}

/**
 * useSegmentColumns 的依赖注入接口
 * 用于列定义中的交互逻辑
 */
export interface SegmentColumnDeps {
  // ── 编辑状态 ──
  inlineEditingSegmentId: Ref<number | null>
  inlineEditForm: SegmentFormModel
  inlineCommentVisible: Ref<number | null>
  inlineCommentText: Ref<string>

  // ── 编辑操作 ──
  startInlineEdit: (segment: Segment) => void
  cancelInlineEdit: () => void
  saveInlineEdit: (segment: Segment) => Promise<void>
  openInlineComment: (segment: Segment) => void
  saveInlineComment: (segment: Segment) => Promise<void>
  closeInlineComment: () => void
  updateCommentText: (value: string) => void
  updateEditFormField: (field: 'source_text' | 'target_text' | 'comment', value: string) => void

  // ── 外部状态 ──
  editingSegmentIds: Ref<number[]>
  onTranslate: (segment: Segment) => void
}

/**
 * 生成段落表格列定义的 composable
 *
 * @param config - 响应式配置，控制哪些列显示以及如何渲染
 * @param deps - 编辑状态和操作的依赖注入
 * @returns 响应式列定义
 */
export function useSegmentColumns(
  config: Ref<SegmentTableConfig>,
  deps: SegmentColumnDeps,
): ComputedRef<DataTableColumns<Segment>> {
  return computed<DataTableColumns<Segment>>(() => {
    const columns: DataTableColumns<Segment> = []

    // ── Selection 列（条件显示） ──
    if (config.value.showSelection) {
      columns.push({
        type: 'selection',
        width: 48,
      })
    }

    // ── Index 列 ──
    columns.push({
      title: '#',
      key: 'segment_index',
      width: 70,
    })

    // ── Source Text 列 ──
    columns.push({
      title: t('workspace.segment.columns.source'),
      key: 'source_text',
      minWidth: 220,
      render: (row) => {
        if (deps.inlineEditingSegmentId.value === row.id) {
          return h(NInput, {
            value: deps.inlineEditForm.source_text,
            type: 'textarea',
            autosize: { minRows: 2, maxRows: 6 },
            'onUpdate:value': (val: string) => deps.updateEditFormField('source_text', val),
          })
        }

        if (config.value.textRenderMode === 'html') {
          return h(HtmlContent, { content: row.source_text, maxLines: 4 })
        }
        return row.source_text
      },
    })

    // ── Target Text 列 ──
    columns.push({
      title: t('workspace.segment.columns.target'),
      key: 'target_text',
      minWidth: 220,
      render: (row) => {
        if (deps.inlineEditingSegmentId.value === row.id) {
          return h(NInput, {
            value: deps.inlineEditForm.target_text,
            type: 'textarea',
            autosize: { minRows: 2, maxRows: 6 },
            placeholder: t('workspace.segment.form.target'),
            'onUpdate:value': (val: string) => deps.updateEditFormField('target_text', val),
          })
        }

        if (!row.target_text) {
          return h(NText, { depth: 3 }, { default: () => t('workspace.segment.emptyTarget') })
        }

        if (config.value.textRenderMode === 'html') {
          return h(HtmlContent, { content: row.target_text, maxLines: 4 })
        }
        return row.target_text
      },
    })

    // ── Status 列 ──
    columns.push({
      title: t('workspace.segment.columns.status'),
      key: 'status',
      width: 110,
      render: (row) =>
        h(
          NTag,
          { size: 'small', type: statusTagType(row.status) },
          { default: () => getSegmentStatusLabel(row.status) },
        ),
    })

    // ── Updated At 列（条件显示） ──
    if (config.value.showUpdatedAt) {
      columns.push({
        title: t('workspace.common.updatedAt'),
        key: 'updated_at',
        width: 170,
        render: (row) => formatDate(row.updated_at),
      })
    }

    // ── Actions 列 ──
    columns.push({
      title: t('workspace.common.actions'),
      key: 'actions',
      width: 220,
      fixed: 'right',
      render: (row) => {
        if (deps.inlineEditingSegmentId.value === row.id) {
          return h(NSpace, { size: 4, wrap: false }, () => [
            h(
              NButton,
              {
                size: 'small',
                quaternary: true,
                onClick: () => deps.cancelInlineEdit(),
              },
              { default: () => t('workspace.segment.actions.cancelInline') },
            ),
            h(
              NButton,
              {
                size: 'small',
                type: 'primary',
                loading: deps.editingSegmentIds.value.includes(row.id),
                onClick: () => deps.saveInlineEdit(row),
              },
              { default: () => t('workspace.segment.actions.saveInline') },
            ),
          ])
        }

        return h(NSpace, { size: 4, wrap: false }, () => [
          // 编辑按钮
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              type: 'primary',
              loading: deps.editingSegmentIds.value.includes(row.id),
              onClick: () => deps.startInlineEdit(row),
            },
            { default: () => t('workspace.segment.actions.edit') },
          ),

          // 评论按钮（条件：showComment）
          ...(config.value.showComment
            ? [
                h(
                  NButton,
                  {
                    size: 'small',
                    quaternary: true,
                    onClick: () => deps.openInlineComment(row),
                  },
                  { default: () => t('workspace.segment.actions.comment') },
                ),
              ]
            : []),

          // 翻译按钮
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              onClick: () => deps.onTranslate(row),
            },
            { default: () => t('workspace.segment.actions.translate') },
          ),
        ])
      },
    })

    return columns
  })
}
