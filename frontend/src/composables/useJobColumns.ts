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
  statusTagType,
  triggerBrowserDownload,
} from '@/composables/useWorkspaceUtils'

type TranslationJob = ApiSchemas['TranslationJob']

export function useJobColumns() {
  const workspace = useProjectWorkspaceStore()

  // ── 任务状态下拉选项 ──
  const jobStatusOptions = computed<SelectOption[]>(() => [
    { label: t('workspace.filters.allStatuses'), value: 'all' },
    { label: t('workspace.job.status.pending'), value: 'pending' },
    { label: t('workspace.job.status.running'), value: 'running' },
    { label: t('workspace.job.status.awaiting_review'), value: 'awaiting_review' },
    { label: t('workspace.job.status.completed'), value: 'completed' },
    { label: t('workspace.job.status.failed'), value: 'failed' },
    { label: t('workspace.job.status.cancelled'), value: 'cancelled' },
  ])

  // ── 列内动作处理器（轻量级，直接调用 store 方法） ──
  const handleOpenJobDetail = async (job: TranslationJob): Promise<void> => {
    workspace.selectedJob = job
    await workspace.loadJobDetail(job.id)
  }

  const handleCancelJob = async (job: TranslationJob): Promise<void> => {
    await workspace.cancelJob(job.id)
  }

  const handleRetryJob = async (job: TranslationJob): Promise<void> => {
    await workspace.retryJob(job.id)
  }

  const handleDownloadJob = async (job: TranslationJob): Promise<void> => {
    const file = await workspace.downloadJobResult(job.id)
    triggerBrowserDownload(file, `translation-job-${job.id}.zip`)
  }

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
      minWidth: 180,
      render: (row) =>
        h(NProgress, {
          type: 'line',
          percentage: getJobProgress(row),
          indicatorPlacement: 'inside',
          processing: row.status === 'pending' || row.status === 'running',
        }),
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
                void handleOpenJobDetail(row)
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
                void handleCancelJob(row)
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
                void handleRetryJob(row)
              },
            },
            { default: () => t('workspace.job.actions.retry') },
          ),
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              type: 'primary',
              disabled: row.status !== 'completed' && row.status !== 'awaiting_review',
              loading: workspace.downloadingKeys.includes(`job:${row.id}:all`),
              onClick: (event: MouseEvent) => {
                event.stopPropagation()
                void handleDownloadJob(row)
              },
            },
            { default: () => t('workspace.common.download') },
          ),
        ]),
    },
  ])

  return { jobColumns, jobStatusOptions }
}
