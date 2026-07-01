import { computed, h } from 'vue'
import type { DataTableColumns, SelectOption } from 'naive-ui'
import { NButton, NProgress, NSpace, NTag, NText } from 'naive-ui'

import { type ApiSchemas } from '@/api/client'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'
import {
  formatDate,
  getJobProgress,
  getJobStatusLabel,
  getJobTriggerLabel,
  getStageLabel,
  statusTagType,
} from '@/composables/useWorkspaceUtils'

type TranslationJob = ApiSchemas['TranslationJob']

export interface JobColumnActions {
  openJobDetail: (job: TranslationJob) => void
  cancelJob: (job: TranslationJob) => void
  retryJob: (job: TranslationJob) => void
}

export function useJobColumns(actions: JobColumnActions) {
  const workspace = useProjectWorkspaceStore()

  // ── 任务状态下拉选项 ──
  const jobStatusOptions = computed<SelectOption[]>(() => [
    { label: t('workspace.filters.allStatuses'), value: 'all' },
    { label: t('workspace.job.status.pending'), value: 'pending' },
    { label: t('workspace.job.status.running'), value: 'running' },
    { label: t('workspace.job.status.completed'), value: 'completed' },
    { label: t('workspace.job.status.failed'), value: 'failed' },
    { label: t('workspace.job.status.cancelled'), value: 'cancelled' },
  ])

  // ── 表格列定义 ──
  const jobColumns = computed<DataTableColumns<TranslationJob>>(() => [
    {
      title: t('workspace.job.columns.id'),
      key: 'id',
      width: 90,
      render: (row) => `#${row.id}`,
    },
    {
      title: t('workspace.job.columns.executionPlan'),
      key: 'execution_plan_id',
      width: 140,
      render: (row) => {
        if (!row.execution_plan_id) {
          return h(NText, { depth: 3, italic: true }, () => t('workspace.job.legacyPlan'))
        }
        return h(NTag, { size: 'small', bordered: false }, () => `#${row.execution_plan_id}`)
      },
    },
    {
      title: t('workspace.job.columns.status'),
      key: 'status',
      width: 140,
      render: (row) =>
        h(
          NTag,
          { size: 'small', type: statusTagType(row.status) },
          { default: () => getJobStatusLabel(row.status) },
        ),
    },
    {
      title: t('workspace.job.columns.progress'),
      key: 'progress',
      width: 220,
      render: (row) => {
        const percent = getJobProgress(row)

        const stageText = row.current_stage ? getStageLabel(row.current_stage) : null

        return h('div', { class: 'flex items-center gap-2 w-full' }, [
          // 阶段标签（如有）
          stageText
            ? h(
                NTag,
                {
                  size: 'tiny',
                  bordered: false,
                  type: 'info',
                  round: true,
                  style: { flexShrink: 0 },
                },
                { default: () => stageText },
              )
            : null,
          // 进度条（flex-1 占据剩余空间，但整体列宽已固定为 220）
          h(NProgress, {
            type: 'line',
            percentage: percent,
            showIndicator: false,
            height: 6,
            borderRadius: 3,
            processing: row.status === 'running',
            status:
              row.status === 'completed'
                ? 'success'
                : row.status === 'failed'
                  ? 'error'
                  : 'default',
            style: { flex: 1, minWidth: 0 },
          }),
          // 百分比数字
          h(
            'span',
            {
              class: 'text-xs font-medium tabular-nums text-lf-text-muted',
              style: { flexShrink: 0, width: '36px', textAlign: 'right' },
            },
            { default: () => `${percent}%` },
          ),
        ])
      },
    },
    {
      title: t('workspace.job.columns.resources'),
      key: 'resource_count',
      width: 130,
      render: (row) => `${row.completed_resources}/${row.resource_count}`,
    },
    {
      title: t('workspace.job.columns.segments'),
      key: 'total_segments',
      width: 130,
      render: (row) => `${row.completed_segments}/${row.total_segments}`,
    },
    {
      title: t('workspace.job.columns.trigger'),
      key: 'trigger_type',
      width: 130,
      render: (row) => getJobTriggerLabel(row.trigger_type),
    },
    {
      title: t('workspace.job.columns.error'),
      key: 'error_message',
      minWidth: 220,
      ellipsis: {
        tooltip: true,
      },
      render: (row) => row.error_message || '-',
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
      render: (row) =>
        h(NSpace, { size: 4, wrap: false }, () => [
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              type: 'primary',
              onClick: (event: MouseEvent) => {
                event.stopPropagation()
                actions.openJobDetail(row)
              },
            },
            { default: () => t('workspace.job.actions.details') },
          ),
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              disabled: row.status !== 'pending' && row.status !== 'running',
              loading: workspace.cancellingJobIds.includes(row.id),
              onClick: (event: MouseEvent) => {
                event.stopPropagation()
                actions.cancelJob(row)
              },
            },
            { default: () => t('workspace.job.actions.cancel') },
          ),
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              disabled: row.status !== 'failed',
              loading: workspace.retryingJobIds.includes(row.id),
              onClick: (event: MouseEvent) => {
                event.stopPropagation()
                actions.retryJob(row)
              },
            },
            { default: () => t('workspace.job.actions.retry') },
          ),
        ]),
    },
  ])

  return { jobColumns, jobStatusOptions }
}
