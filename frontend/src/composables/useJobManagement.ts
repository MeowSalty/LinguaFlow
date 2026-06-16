import { computed, h, reactive, ref, type Ref } from 'vue'
import type { DataTableColumns, FormInst, FormRules, SelectOption } from 'naive-ui'
import { NButton, NProgress, NSpace, NTag, NText, useMessage } from 'naive-ui'

import { type ApiSchemas } from '@/api/client'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
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

type Segment = ApiSchemas['Segment']
type TranslationJob = ApiSchemas['TranslationJob']
type CreateTranslationJobPayload = ApiSchemas['CreateTranslationJobRequest']

export type JobTargetMode = 'resources' | 'segments'

export interface JobFormModel {
  execution_plan_id: number | null
}

export function useJobManagement(
  projectId: Ref<number | null>,
  onJobCreated?: () => Promise<void>,
) {
  const message = useMessage()
  const workspace = useProjectWorkspaceStore()
  const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()

  // ── 状态 ──
  const jobDrawerVisible = ref(false)
  const jobFormRef = ref<FormInst | null>(null)
  const jobDetailDrawerVisible = ref(false)
  const jobTargetMode = ref<JobTargetMode>('resources')
  const jobTargetResourceIds = ref<number[]>([])
  const jobTargetSegmentIds = ref<number[]>([])

  const jobForm = reactive<JobFormModel>({
    execution_plan_id: null,
  })

  // ── 计算属性 ──
  const executionPlanOptions = computed(() =>
    executionPlanTemplatesStore.items.map((item) => ({
      label: `${item.name} (${item.rounds?.length ?? 0} 轮)`,
      value: item.id,
    })),
  )

  const jobFormRules = computed<FormRules>(() => ({
    execution_plan_id: [
      {
        required: true,
        type: 'number',
        message: t('workspace.job.validation.executionPlanRequired'),
        trigger: ['change', 'blur'],
      },
    ],
  }))

  const jobStatusOptions = computed<SelectOption[]>(() => [
    { label: t('workspace.filters.allStatuses'), value: 'all' },
    { label: t('workspace.job.status.pending'), value: 'pending' },
    { label: t('workspace.job.status.running'), value: 'running' },
    { label: t('workspace.job.status.awaiting_review'), value: 'awaiting_review' },
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
                void openJobDetail(row)
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
                void cancelJob(row)
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
                void retryJob(row)
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
                void downloadJob(row)
              },
            },
            { default: () => t('workspace.common.download') },
          ),
        ]),
    },
  ])

  // ── 方法 ──
  const selectedReadyResourceIds = computed(() =>
    workspace.selectedResources
      .filter((resource) => resource.status === 'ready')
      .map((resource) => resource.id),
  )

  const canCreateResourceJob = computed(() => selectedReadyResourceIds.value.length > 0)
  const canCreateSegmentJob = computed(() => workspace.segments.length > 0)

  const openResourceJobDrawer = (): void => {
    if (!canCreateResourceJob.value) {
      message.warning(t('workspace.messages.selectReadyResource'))
      return
    }

    jobTargetMode.value = 'resources'
    jobTargetResourceIds.value = selectedReadyResourceIds.value
    jobTargetSegmentIds.value = []
    jobForm.execution_plan_id = null
    jobDrawerVisible.value = true
  }

  const openSegmentJobDrawer = (segment?: Segment): void => {
    if (!workspace.activeResourceId) {
      message.warning(t('workspace.messages.selectResourceFirst'))
      return
    }

    jobTargetMode.value = 'segments'
    jobTargetResourceIds.value = [workspace.activeResourceId]
    jobTargetSegmentIds.value = segment ? [segment.id] : workspace.segments.map((item) => item.id)
    jobForm.execution_plan_id = null
    jobDrawerVisible.value = true
  }

  const closeJobDrawer = (): void => {
    jobDrawerVisible.value = false
    jobTargetResourceIds.value = []
    jobTargetSegmentIds.value = []
    jobForm.execution_plan_id = null
  }

  const submitJob = async (): Promise<void> => {
    if (!projectId.value || !jobForm.execution_plan_id) {
      return
    }

    const payload: CreateTranslationJobPayload = {
      execution_plan_id: jobForm.execution_plan_id,
      resource_ids: jobTargetResourceIds.value,
    }

    if (jobTargetMode.value === 'segments') {
      payload.segment_ids = jobTargetSegmentIds.value
    }

    try {
      await workspace.createJob(projectId.value, payload)
      message.success(t('workspace.messages.jobCreated'))
      closeJobDrawer()
      if (onJobCreated) {
        await onJobCreated()
      }
      await workspace.loadJobs(projectId.value)
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.jobCreateFailed'))
    }
  }

  const cancelJob = async (job: TranslationJob): Promise<void> => {
    try {
      await workspace.cancelJob(job.id)
      message.success(t('workspace.messages.jobCancelled'))
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.jobCancelFailed'))
    }
  }

  const retryJob = async (job: TranslationJob): Promise<void> => {
    try {
      await workspace.retryJob(job.id)
      message.success(t('workspace.messages.jobRetried'))
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.jobRetryFailed'))
    }
  }

  const downloadJob = async (job: TranslationJob): Promise<void> => {
    try {
      const file = await workspace.downloadJobResult(job.id)
      triggerBrowserDownload(file, `translation-job-${job.id}.zip`)
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.downloadFailed'))
    }
  }

  const openJobDetail = async (job: TranslationJob): Promise<void> => {
    jobDetailDrawerVisible.value = true
    workspace.selectedJob = job

    try {
      await workspace.loadJobDetail(job.id)
    } catch (error) {
      console.error(error)
      message.error(workspace.jobDetailError || t('workspace.messages.jobDetailFailed'))
    }
  }

  return {
    // 状态
    jobDrawerVisible,
    jobFormRef,
    jobDetailDrawerVisible,
    jobTargetMode,
    jobTargetResourceIds,
    jobTargetSegmentIds,
    jobForm,
    // 计算属性
    executionPlanOptions,
    jobFormRules,
    jobStatusOptions,
    jobColumns,
    selectedReadyResourceIds,
    canCreateResourceJob,
    canCreateSegmentJob,
    // 方法
    openResourceJobDrawer,
    openSegmentJobDrawer,
    closeJobDrawer,
    submitJob,
    cancelJob,
    retryJob,
    downloadJob,
    openJobDetail,
  }
}
