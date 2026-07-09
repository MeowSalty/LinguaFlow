import type { DataTableColumns } from 'naive-ui'
import { NButton, NIcon, NInput, NSpace, NTag, NText, NTooltip } from 'naive-ui'
import type { ComputedRef, Ref, VNode } from 'vue'
import { computed, h } from 'vue'

import IconCarbonEdit from '~icons/carbon/edit'
import IconCarbonChat from '~icons/carbon/chat'
import IconCarbonLanguage from '~icons/carbon/language'
import IconCarbonCheckmark from '~icons/carbon/checkmark'
import IconCarbonClose from '~icons/carbon/close'
import IconCarbonChevronDown from '~icons/carbon/chevron-down'
import IconCarbonCircleDash from '~icons/carbon/circle-dash'
import IconCarbonWarning from '~icons/carbon/warning'
import IconCarbonError from '~icons/carbon/error'

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
  saveAndEditNext: (segment: Segment) => Promise<void>
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
      width: 50,
      align: 'center',
    })

    // ── Source Text 列 ──
    columns.push({
      title: t('workspace.segment.columns.source'),
      key: 'source_text',
      minWidth: 280,
      render: (row) => {
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
      minWidth: 280,
      render: (row) => {
        const elements: VNode[] = []

        // 编辑态：译文输入框
        if (deps.inlineEditingSegmentId.value === row.id) {
          elements.push(
            h(NInput, {
              value: deps.inlineEditForm.target_text,
              type: 'textarea',
              autosize: { minRows: 2, maxRows: 6 },
              placeholder: t('workspace.segment.form.target'),
              'onUpdate:value': (val: string) => deps.updateEditFormField('target_text', val),
            }),
          )
        } else {
          // 非编辑态：译文展示
          if (!row.target_text) {
            elements.push(
              h('div', { class: 'target-empty' }, [
                h(NText, { depth: 3 }, { default: () => t('workspace.segment.emptyTarget') }),
              ]),
            )
          } else if (config.value.textRenderMode === 'html') {
            elements.push(h(HtmlContent, { content: row.target_text, maxLines: 4 }))
          } else {
            elements.push(h('span', null, row.target_text))
          }
        }

        // 质量问题图标 + 评论摘要（同一行显示）
        const metaElements: VNode[] = []

        if (row.quality_issues && row.quality_issues.length > 0) {
          for (const issue of row.quality_issues) {
            const isError = issue.severity === 'error'
            metaElements.push(
              h(
                NTooltip,
                {},
                {
                  trigger: () =>
                    h(
                      NIcon,
                      { size: 14, color: isError ? '#d03050' : '#f0a020' },
                      { default: () => h(isError ? IconCarbonError : IconCarbonWarning) },
                    ),
                  default: () => issue.message,
                },
              ),
            )
          }
        }

        if (
          config.value.showComment &&
          row.review_comment &&
          deps.inlineCommentVisible.value !== row.id
        ) {
          metaElements.push(
            h(NIcon, { size: 14 }, { default: () => h(IconCarbonChat) }),
            h('span', { class: 'truncate max-w-[200px]' }, row.review_comment),
          )
        }

        if (metaElements.length > 0) {
          elements.push(
            h(
              'div',
              { class: 'mt-1 flex items-center gap-1 text-xs text-lf-text-muted' },
              metaElements,
            ),
          )
        }

        // 评论区域（行内展开）
        if (config.value.showComment && deps.inlineCommentVisible.value === row.id) {
          elements.push(
            h(
              'div',
              { class: 'mt-2 rounded-lg border border-lf-border-soft bg-lf-surface-muted p-3' },
              [
                h(
                  'div',
                  { class: 'mb-2 text-xs text-lf-text-muted' },
                  t('workspace.segment.form.comment'),
                ),
                h(NInput, {
                  value: deps.inlineCommentText.value,
                  type: 'textarea',
                  autosize: { minRows: 2, maxRows: 4 },
                  placeholder: t('workspace.segment.form.comment'),
                  'onUpdate:value': (val: string) => deps.updateCommentText(val),
                }),
                h('div', { class: 'mt-2 flex justify-end gap-2' }, [
                  h(
                    NButton,
                    { size: 'small', onClick: () => deps.closeInlineComment() },
                    { default: () => t('workspace.segment.actions.cancelInline') },
                  ),
                  h(
                    NButton,
                    {
                      size: 'small',
                      type: 'primary',
                      onClick: () => deps.saveInlineComment(row),
                    },
                    { default: () => t('workspace.common.save') },
                  ),
                ]),
              ],
            ),
          )
        }

        return elements.length === 1 ? elements[0] : h('div', { class: 'space-y-1' }, elements)
      },
    })

    // ── Status 列 ──
    columns.push({
      title: t('workspace.segment.columns.status'),
      key: 'status',
      width: 110,
      render: (row) => {
        const iconMap: Record<string, typeof IconCarbonCircleDash> = {
          pending: IconCarbonCircleDash,
          translated: IconCarbonCheckmark,
          edited: IconCarbonEdit,
          approved: IconCarbonCheckmark,
          rejected: IconCarbonClose,
        }
        const icon = iconMap[row.status] ?? IconCarbonCircleDash
        return h(
          NTag,
          { size: 'small', type: statusTagType(row.status) },
          {
            default: () => getSegmentStatusLabel(row.status),
            icon: () => h(NIcon, { size: 14 }, { default: () => h(icon) }),
          },
        )
      },
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
      width: 160,
      fixed: 'right',
      render: (row) => {
        if (deps.inlineEditingSegmentId.value === row.id) {
          return h(NSpace, { size: 4, wrap: false }, () => [
            h(
              NTooltip,
              { placement: 'top' },
              {
                trigger: () =>
                  h(
                    NButton,
                    {
                      size: 'small',
                      quaternary: true,
                      onClick: () => deps.cancelInlineEdit(),
                    },
                    { icon: () => h(NIcon, null, { default: () => h(IconCarbonClose) }) },
                  ),
                default: () => t('workspace.segment.actions.cancelInline'),
              },
            ),
            h(
              NTooltip,
              { placement: 'top' },
              {
                trigger: () =>
                  h(
                    NButton,
                    {
                      size: 'small',
                      type: 'primary',
                      loading: deps.editingSegmentIds.value.includes(row.id),
                      onClick: () => deps.saveInlineEdit(row),
                    },
                    { icon: () => h(NIcon, null, { default: () => h(IconCarbonCheckmark) }) },
                  ),
                default: () => t('workspace.segment.actions.saveInline'),
              },
            ),
            h(
              NTooltip,
              { placement: 'top' },
              {
                trigger: () =>
                  h(
                    NButton,
                    {
                      size: 'small',
                      type: 'primary',
                      loading: deps.editingSegmentIds.value.includes(row.id),
                      onClick: () => deps.saveAndEditNext(row),
                    },
                    {
                      icon: () => h(NIcon, null, { default: () => h(IconCarbonChevronDown) }),
                    },
                  ),
                default: () => t('workspace.segment.actions.saveAndNext'),
              },
            ),
          ])
        }

        return h(NSpace, { size: 4, wrap: false }, () => [
          h(
            NTooltip,
            { placement: 'top' },
            {
              trigger: () =>
                h(
                  NButton,
                  {
                    size: 'small',
                    quaternary: true,
                    type: 'primary',
                    loading: deps.editingSegmentIds.value.includes(row.id),
                    onClick: () => deps.startInlineEdit(row),
                  },
                  { icon: () => h(NIcon, null, { default: () => h(IconCarbonEdit) }) },
                ),
              default: () => t('workspace.segment.actions.edit'),
            },
          ),

          ...(config.value.showComment
            ? [
                h(
                  NTooltip,
                  { placement: 'top' },
                  {
                    trigger: () =>
                      h(
                        NButton,
                        {
                          size: 'small',
                          quaternary: true,
                          onClick: () => deps.openInlineComment(row),
                        },
                        { icon: () => h(NIcon, null, { default: () => h(IconCarbonChat) }) },
                      ),
                    default: () => t('workspace.segment.actions.comment'),
                  },
                ),
              ]
            : []),

          h(
            NTooltip,
            { placement: 'top' },
            {
              trigger: () =>
                h(
                  NButton,
                  {
                    size: 'small',
                    quaternary: true,
                    onClick: () => deps.onTranslate(row),
                  },
                  { icon: () => h(NIcon, null, { default: () => h(IconCarbonLanguage) }) },
                ),
              default: () => t('workspace.segment.actions.translate'),
            },
          ),
        ])
      },
    })

    return columns
  })
}
