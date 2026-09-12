import type { DataTableColumns } from 'naive-ui'
import { NButton, NIcon, NInput, NPopover, NSpace, NTag, NTooltip } from 'naive-ui'
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
import IconCarbonUndo from '~icons/carbon/undo'
import IconCarbonTrashCan from '~icons/carbon/trash-can'
import IconCarbonMagicWand from '~icons/carbon/magic-wand'

import type { ApiSchemas } from '@/api/client'
import type { SegmentFormModel } from '@/composables/useSegmentEditing'
import {
  type QualityIssue,
  formatQualityIssueTooltip,
  hasPendingSemanticIssues,
  isIssueDismissed,
  resolveActiveIssueIndex,
} from '@/composables/useQualityIssues'
import {
  renderSearchHighlightedHtml,
  renderSearchHighlightedText,
  type SearchMatchOptions,
} from '@/composables/useSearchHighlight'
import { getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import SegmentTextDisplay from '@/components/workspace/SegmentTextDisplay.vue'
import { t } from '@/i18n'
import { formatDateTime } from '@/utils/datetime'

type Segment = ApiSchemas['Segment']

/** 段落是否可发起单段修订（已翻译/已编辑、译文非空、存在 pending 语义 issue） */
const canPreviewRevision = (segment: Segment): boolean =>
  (segment.status === 'translated' || segment.status === 'edited') &&
  Boolean(segment.target_text) &&
  hasPendingSemanticIssues(segment)

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

  // ── 原文 HTML 切换 ──
  showSourceHtml: Ref<boolean>
  toggleSourceHtml: () => void

  // ── 编辑操作 ──
  startInlineEdit: (segment: Segment) => void
  cancelInlineEdit: () => void
  saveInlineEdit: (segment: Segment) => Promise<void>
  saveAndEditNext: (segment: Segment) => Promise<void>
  openInlineComment: (segment: Segment) => void
  saveInlineComment: (segment: Segment) => Promise<void>
  closeInlineComment: () => void
  updateCommentText: (value: string) => void
  updateEditFormField: (field: 'target_text' | 'comment', value: string) => void

  // ── 搜索定位联动 ──
  /** 搜索定位面板当前关键词（激活时源文/译文列以搜索高亮渲染，替代质量标记） */
  searchQuery: Ref<string>
  /** 搜索字段范围：被排除的列不做搜索高亮，走普通文本 / 质量高亮路径 */
  searchField: Ref<'source' | 'target' | 'both'>
  /** 展示级匹配选项（模式 / 全字 / 大小写），与搜索定位面板同源 */
  searchMatchOptions: Ref<SearchMatchOptions>

  // ── 外部状态 ──
  editingSegmentIds: Ref<number[]>
  onPreviewTranslation: (segment: Segment) => void
  onPreviewRevision: (segment: Segment) => void

  // ── 质量问题裁决 ──
  /** 对某条质量问题下驳回裁决（dismissed） */
  onDismissIssue: (segment: Segment, issue: QualityIssue) => void
  /** 撤销某条质量问题的裁决（改回 pending） */
  onReinstateIssue: (segment: Segment, issue: QualityIssue) => void

  // ── 质量问题高亮联动（HTML 模式） ──
  /** 当前悬停的问题，格式 `${segmentId}:${issueIndex}` */
  hoveredIssueKey: Ref<string | null>
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
      // 内边距后需容纳三位数编号不折行（曾出现 124 折成 12/4）
      width: 64,
      align: 'center',
    })

    // ── Source Text 列 ──
    columns.push({
      title: t('workspace.segment.columns.source'),
      key: 'source_text',
      minWidth: 260,
      render: (row) => {
        const isEditing = deps.inlineEditingSegmentId.value === row.id
        const hasHtmlTags =
          config.value.textRenderMode === 'html' && /<[a-z][\s\S]*>/i.test(row.source_text)

        const elements: VNode[] = []

        const query = deps.searchQuery.value.trim()
        // 搜索字段排除源文时不做搜索高亮，避免「无命中」的源文列仍按搜索路径渲染
        if (query && deps.searchField.value !== 'target') {
          // 搜索激活：以搜索命中高亮渲染（搜索期间替代质量标记，避免双重 mark 噪声）
          elements.push(
            config.value.textRenderMode === 'html'
              ? renderSearchHighlightedHtml(row.source_text, query, deps.searchMatchOptions.value)
              : renderSearchHighlightedText(row.source_text, query, deps.searchMatchOptions.value),
          )
        } else {
          elements.push(
            h(SegmentTextDisplay, {
              text: row.source_text,
              mode: config.value.textRenderMode,
            }),
          )
        }

        if (isEditing && hasHtmlTags) {
          elements.push(
            h('div', { class: 'mt-1.5' }, [
              h(
                NButton,
                {
                  size: 'tiny',
                  quaternary: true,
                  type: deps.showSourceHtml.value ? 'primary' : 'default',
                  onClick: (e: MouseEvent) => {
                    e.stopPropagation()
                    deps.toggleSourceHtml()
                  },
                },
                {
                  default: () =>
                    h(
                      'span',
                      { class: 'font-mono text-xs' },
                      deps.showSourceHtml.value
                        ? t('workspace.segment.actions.hideSourceHtml')
                        : t('workspace.segment.actions.viewSourceHtml'),
                    ),
                },
              ),
            ]),
          )

          if (deps.showSourceHtml.value) {
            elements.push(
              h(
                'pre',
                {
                  class:
                    'mt-2 max-h-40 overflow-auto rounded-md bg-lf-surface-muted p-2.5 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all',
                },
                row.source_text,
              ),
            )
          }
        }

        return elements.length === 1 ? elements[0] : h('div', null, elements)
      },
    })

    // ── Target Text 列 ──
    columns.push({
      title: t('workspace.segment.columns.target'),
      key: 'target_text',
      minWidth: 260,
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
          if (!row.target_text) {
            elements.push(
              h(
                'div',
                {
                  class:
                    'flex min-h-10 items-center justify-center rounded-lf-ctl border border-dashed border-lf-border-soft bg-lf-info-soft px-3 py-2',
                },
                [h('span', { class: 'text-lf-text-subtle' }, t('workspace.segment.emptyTarget'))],
              ),
            )
          } else {
            const query = deps.searchQuery.value.trim()
            // 搜索字段排除译文时不做搜索高亮，保持质量问题标记路径
            if (query && deps.searchField.value !== 'source') {
              // 搜索激活：以搜索命中高亮渲染（同源文列，替代质量标记）
              elements.push(
                config.value.textRenderMode === 'html'
                  ? renderSearchHighlightedHtml(
                      row.target_text,
                      query,
                      deps.searchMatchOptions.value,
                    )
                  : renderSearchHighlightedText(
                      row.target_text,
                      query,
                      deps.searchMatchOptions.value,
                    ),
              )
            } else {
              const activeIssueIndex = resolveActiveIssueIndex(deps.hoveredIssueKey.value, row.id)
              elements.push(
                h(SegmentTextDisplay, {
                  text: row.target_text,
                  issues: row.quality_issues,
                  mode: config.value.textRenderMode,
                  activeIssueIndex,
                }),
              )
            }
          }
        }

        // 质量问题图标 + 评论摘要（同一行显示）
        const metaElements: VNode[] = []

        if (row.quality_issues && row.quality_issues.length > 0) {
          // HTML 模式下悬停图标联动强调对应高亮区间
          const linkable = config.value.textRenderMode === 'html'
          row.quality_issues.forEach((issue, issueIndex) => {
            const isError = issue.severity === 'error'
            const dismissed = isIssueDismissed(issue)
            const issueKey = `${row.id}:${issueIndex}`
            const hoverProps = linkable
              ? {
                  onMouseenter: () => {
                    deps.hoveredIssueKey.value = issueKey
                  },
                  onMouseleave: () => {
                    if (deps.hoveredIssueKey.value === issueKey) {
                      deps.hoveredIssueKey.value = null
                    }
                  },
                }
              : {}
            // 已驳回的问题用灰色淡化图标，区分未决问题
            const iconColor = dismissed ? '#9ca3af' : isError ? '#d03050' : '#f0a020'
            metaElements.push(
              h(
                NPopover,
                {
                  style: { maxWidth: '320px' },
                  placement: 'bottom',
                  trigger: 'hover',
                  delay: 150,
                },
                {
                  trigger: () =>
                    h(
                      NIcon,
                      { size: 14, color: iconColor, ...hoverProps },
                      {
                        default: () =>
                          h(
                            dismissed
                              ? IconCarbonCircleDash
                              : isError
                                ? IconCarbonError
                                : IconCarbonWarning,
                          ),
                      },
                    ),
                  default: () =>
                    h('div', { class: 'space-y-2' }, [
                      h(
                        'div',
                        { class: 'whitespace-pre-line text-xs leading-relaxed' },
                        formatQualityIssueTooltip(issue),
                      ),
                      h(
                        NButton,
                        {
                          size: 'tiny',
                          quaternary: true,
                          type: dismissed ? 'default' : 'warning',
                          disabled: deps.editingSegmentIds.value.includes(row.id),
                          onClick: (e: MouseEvent) => {
                            e.stopPropagation()
                            if (dismissed) {
                              deps.onReinstateIssue(row, issue)
                            } else {
                              deps.onDismissIssue(row, issue)
                            }
                          },
                        },
                        {
                          icon: () =>
                            h(
                              NIcon,
                              { size: 12 },
                              { default: () => h(dismissed ? IconCarbonUndo : IconCarbonTrashCan) },
                            ),
                          default: () =>
                            dismissed
                              ? t('workspace.segment.disposition.reinstateAction')
                              : t('workspace.segment.disposition.dismissAction'),
                        },
                      ),
                    ]),
                },
              ),
            )
          })
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
                    { default: () => t('common.save') },
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
      width: 100,
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
        title: t('common.updatedAt'),
        key: 'updated_at',
        width: 95,
        render: (row) => {
          if (!row.updated_at) {
            return h('span', { class: 'text-lf-text-muted' }, t('common.noDate'))
          }
          const date = new Date(row.updated_at)
          const dateStr = formatDateTime(date, {
            year: 'numeric',
            month: '2-digit',
            day: '2-digit',
          })
          const timeStr = formatDateTime(date, {
            hour: '2-digit',
            minute: '2-digit',
          })
          return h('div', { class: 'leading-tight' }, [
            h('div', { class: 'text-xs text-lf-text-muted' }, dateStr),
            h('div', { class: 'text-sm' }, timeStr),
          ])
        },
      })
    }

    // ── Actions 列 ──
    columns.push({
      title: t('common.actionsColumn'),
      key: 'actions',
      width: 144,
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
                    onClick: () => deps.onPreviewTranslation(row),
                  },
                  { icon: () => h(NIcon, null, { default: () => h(IconCarbonLanguage) }) },
                ),
              default: () => t('workspace.segment.actions.previewTranslation'),
            },
          ),

          ...(canPreviewRevision(row)
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
                          type: 'primary',
                          onClick: () => deps.onPreviewRevision(row),
                        },
                        { icon: () => h(NIcon, null, { default: () => h(IconCarbonMagicWand) }) },
                      ),
                    default: () => t('workspace.segment.actions.previewRevision'),
                  },
                ),
              ]
            : []),
        ])
      },
    })

    return columns
  })
}
