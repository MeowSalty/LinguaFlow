<script setup lang="ts">
import {
  NButton,
  NPopconfirm,
  NProgress,
  NSpace,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
  type SelectOption,
  type UploadCustomRequestOptions,
} from 'naive-ui'
import { h } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas, type DownloadFileResult } from '@/api/client'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type Resource = ApiSchemas['Resource']
type Segment = ApiSchemas['Segment']
type TranslationJob = ApiSchemas['TranslationJob']
type CreateTranslationJobPayload = ApiSchemas['CreateTranslationJobRequest']

type WorkspaceTab = 'resources' | 'segments' | 'jobs'
type JobTargetMode = 'resources' | 'segments'

interface SegmentFormModel {
  source_text: string
  target_text: string
  comment: string
}

interface JobFormModel {
  source_lang: string
  target_lang: string
  backend_order_text: string
}

const route = useRoute()
const router = useRouter()
const message = useMessage()
const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

const activeTab = ref<WorkspaceTab>('resources')
const editingSegment = ref<Segment | null>(null)
const segmentDrawerVisible = ref(false)
const jobDrawerVisible = ref(false)
const jobDetailDrawerVisible = ref(false)
const jobTargetMode = ref<JobTargetMode>('resources')
const jobTargetResourceIds = ref<number[]>([])
const jobTargetSegmentIds = ref<number[]>([])
const replacingResourceId = ref<number | null>(null)
const uploadDismissTimers = new Map<string, ReturnType<typeof setTimeout>>()

const segmentForm = reactive<SegmentFormModel>({
  source_text: '',
  target_text: '',
  comment: '',
})

const jobForm = reactive<JobFormModel>({
  source_lang: '',
  target_lang: '',
  backend_order_text: '',
})

const projectId = computed(() => {
  const params = route.params as Partial<Record<'projectId', string | string[]>>
  const rawValue = Array.isArray(params.projectId) ? params.projectId[0] : params.projectId
  const parsed = Number(rawValue)

  return Number.isFinite(parsed) ? parsed : null
})

const resourceStatusOptions = computed<SelectOption[]>(() => [
  { label: t('workspace.filters.allStatuses'), value: 'all' },
  { label: t('workspace.resource.status.ready'), value: 'ready' },
  { label: t('workspace.resource.status.error'), value: 'error' },
])

const resourceFormatOptions = computed<SelectOption[]>(() => [
  { label: t('workspace.filters.allFormats'), value: 'all' },
  ...workspace.availableFormats.map((format) => ({
    label: format,
    value: format,
  })),
])

const segmentStatusOptions = computed<SelectOption[]>(() => [
  { label: t('workspace.filters.allStatuses'), value: 'all' },
  { label: t('workspace.segment.status.pending'), value: 'pending' },
  { label: t('workspace.segment.status.translated'), value: 'translated' },
  { label: t('workspace.segment.status.reviewed'), value: 'reviewed' },
  { label: t('workspace.segment.status.rejected'), value: 'rejected' },
])

const jobStatusOptions = computed<SelectOption[]>(() => [
  { label: t('workspace.filters.allStatuses'), value: 'all' },
  { label: t('workspace.job.status.pending'), value: 'pending' },
  { label: t('workspace.job.status.running'), value: 'running' },
  { label: t('workspace.job.status.awaiting_review'), value: 'awaiting_review' },
  { label: t('workspace.job.status.completed'), value: 'completed' },
  { label: t('workspace.job.status.failed'), value: 'failed' },
  { label: t('workspace.job.status.cancelled'), value: 'cancelled' },
])

const selectedReadyResourceIds = computed(() =>
  workspace.selectedResources
    .filter((resource) => resource.status === 'ready')
    .map((resource) => resource.id),
)

const canCreateResourceJob = computed(() => selectedReadyResourceIds.value.length > 0)
const canCreateSegmentJob = computed(() =>
  Boolean(editingSegment.value || workspace.segments.length > 0),
)

const formatDate = (value?: string): string => {
  if (!value) {
    return t('workspace.common.noDate')
  }

  return new Intl.DateTimeFormat('zh-Hans', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

const statusTagType = (status: string): 'default' | 'success' | 'warning' | 'error' | 'info' => {
  switch (status) {
    case 'ready':
    case 'completed':
    case 'translated':
    case 'reviewed':
      return 'success'
    case 'processing':
    case 'pending':
    case 'running':
    case 'awaiting_review':
      return 'info'
    case 'error':
    case 'failed':
    case 'rejected':
      return 'error'
    case 'cancelled':
      return 'warning'
    default:
      return 'default'
  }
}

const getResourceStatusLabel = (status: Resource['status']): string =>
  t(`workspace.resource.status.${status}`)

const getSegmentStatusLabel = (status: string): string =>
  t(`workspace.segment.status.${status}`, status)

const getJobStatusLabel = (status: TranslationJob['status']): string =>
  t(`workspace.job.status.${status}`)

const getJobTriggerLabel = (trigger: TranslationJob['trigger_type']): string =>
  t(`workspace.job.trigger.${trigger}`)

const getJobProgress = (job: TranslationJob): number => {
  if (job.total_segments <= 0) {
    return job.status === 'completed' ? 100 : 0
  }

  return Math.round((job.completed_segments / job.total_segments) * 100)
}

const triggerBrowserDownload = (file: DownloadFileResult, fallbackName: string): void => {
  const url = URL.createObjectURL(file.blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = file.filename || fallbackName
  anchor.click()
  URL.revokeObjectURL(url)
}

const reloadWorkspace = async (): Promise<void> => {
  if (!projectId.value) {
    return
  }

  await Promise.all([
    workspace.loadProject(projectId.value),
    workspace.loadResources(projectId.value),
    workspace.loadJobs(projectId.value),
  ])
}

const reloadSegments = async (): Promise<void> => {
  if (!projectId.value || !workspace.activeResourceId) {
    return
  }

  await workspace.loadSegments(projectId.value, workspace.activeResourceId)
}

const scheduleTaskDismiss = (taskId: string): void => {
  const timer = setTimeout(() => {
    uploadDismissTimers.delete(taskId)
    workspace.removeUploadTask(taskId)
  }, 3000)
  uploadDismissTimers.set(taskId, timer)
}

const dismissTask = (taskId: string): void => {
  const timer = uploadDismissTimers.get(taskId)
  if (timer) {
    clearTimeout(timer)
    uploadDismissTimers.delete(taskId)
  }
  workspace.removeUploadTask(taskId)
}

const handleUpload = async ({
  file,
  onFinish,
  onError,
}: UploadCustomRequestOptions): Promise<void> => {
  if (!projectId.value || !file.file) {
    onError()
    return
  }

  const taskId = workspace.addUploadTask(file.name)

  try {
    await workspace.uploadResources(projectId.value, [file.file], taskId)
    message.success(t('workspace.messages.uploadSuccess'))
    onFinish()
    workspace.updateUploadTaskStage(taskId, 'complete', undefined)
    scheduleTaskDismiss(taskId)
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.uploadFailed'))
    onError()
  }
}

const chooseReplacementFile = (resourceId: number): void => {
  const input = document.createElement('input')
  input.type = 'file'
  input.onchange = () => {
    const file = input.files?.[0]
    if (file) {
      void replaceResource(resourceId, file)
    }
  }
  input.click()
}

const replaceResource = async (resourceId: number, file: File): Promise<void> => {
  if (!projectId.value) {
    return
  }

  replacingResourceId.value = resourceId
  try {
    await workspace.replaceResource(projectId.value, resourceId, file)
    message.success(t('workspace.messages.replaceSuccess'))
    if (workspace.activeResourceId === resourceId) {
      await reloadSegments()
    }
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.replaceFailed'))
  } finally {
    replacingResourceId.value = null
  }
}

const deleteResource = async (resource: Resource): Promise<void> => {
  if (!projectId.value) {
    return
  }

  try {
    await workspace.deleteResource(projectId.value, resource.id)
    message.success(t('workspace.messages.deleteResourceSuccess'))
    if (workspace.activeResourceId) {
      await reloadSegments()
    }
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.deleteResourceFailed'))
  }
}

const downloadResource = async (resource: Resource): Promise<void> => {
  if (!projectId.value) {
    return
  }

  try {
    const file = await workspace.downloadResource(projectId.value, resource.id)
    triggerBrowserDownload(file, resource.filename)
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.downloadFailed'))
  }
}

const openSegmentsForResource = async (resource: Resource): Promise<void> => {
  workspace.setActiveResource(resource.id)
  activeTab.value = 'segments'
  await reloadSegments()
}

const openSegmentDrawer = (segment: Segment): void => {
  editingSegment.value = segment
  segmentForm.source_text = segment.source_text
  segmentForm.target_text = segment.target_text ?? ''
  segmentForm.comment = segment.review_comment ?? ''
  segmentDrawerVisible.value = true
}

const closeSegmentDrawer = (): void => {
  segmentDrawerVisible.value = false
  editingSegment.value = null
  segmentForm.source_text = ''
  segmentForm.target_text = ''
  segmentForm.comment = ''
}

const saveSegment = async (): Promise<void> => {
  if (!projectId.value || !workspace.activeResourceId || !editingSegment.value) {
    return
  }

  try {
    await workspace.updateSegment(
      projectId.value,
      workspace.activeResourceId,
      editingSegment.value.id,
      {
        source_text: segmentForm.source_text,
        target_text: segmentForm.target_text || undefined,
        comment: segmentForm.comment || undefined,
      },
    )
    message.success(t('workspace.messages.segmentSaved'))
    closeSegmentDrawer()
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.segmentSaveFailed'))
  }
}

const setDefaultJobForm = (): void => {
  jobForm.source_lang = workspace.project?.source_lang || 'auto'
  jobForm.target_lang = workspace.project?.target_lang || 'en-US'
  const config = workspace.project?.default_translation_config
  const backendOrder = Array.isArray(config?.backend_order) ? config.backend_order : []
  jobForm.backend_order_text = backendOrder.join('\n')
}

const openResourceJobDrawer = (): void => {
  if (!canCreateResourceJob.value) {
    message.warning(t('workspace.messages.selectReadyResource'))
    return
  }

  jobTargetMode.value = 'resources'
  jobTargetResourceIds.value = selectedReadyResourceIds.value
  jobTargetSegmentIds.value = []
  setDefaultJobForm()
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
  setDefaultJobForm()
  jobDrawerVisible.value = true
}

const closeJobDrawer = (): void => {
  jobDrawerVisible.value = false
  jobTargetResourceIds.value = []
  jobTargetSegmentIds.value = []
  jobForm.source_lang = ''
  jobForm.target_lang = ''
  jobForm.backend_order_text = ''
}

const submitJob = async (): Promise<void> => {
  if (!projectId.value) {
    return
  }

  const backendOrder = jobForm.backend_order_text
    .split(/\r?\n|,/)
    .map((item) => item.trim())
    .filter(Boolean)
  const payload: CreateTranslationJobPayload = {
    resource_ids: jobTargetResourceIds.value,
    translation_config: {
      source_lang: jobForm.source_lang.trim(),
      target_lang: jobForm.target_lang.trim(),
      backend_order: backendOrder,
    },
  }

  if (jobTargetMode.value === 'segments') {
    payload.segment_ids = jobTargetSegmentIds.value
  }

  try {
    await workspace.createJob(projectId.value, payload)
    message.success(t('workspace.messages.jobCreated'))
    closeJobDrawer()
    activeTab.value = 'jobs'
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

const formatConfigValue = (value: unknown): string => {
  if (value === null || value === undefined || value === '') {
    return '-'
  }

  if (Array.isArray(value)) {
    return value.length > 0 ? value.join(', ') : '-'
  }

  if (typeof value === 'object') {
    return JSON.stringify(value, null, 2)
  }

  return String(value)
}

const resourceColumns = computed<DataTableColumns<Resource>>(() => [
  {
    type: 'selection',
    disabled: (row) => row.status !== 'ready',
  },
  {
    title: t('workspace.resource.columns.filename'),
    key: 'filename',
    minWidth: 220,
    ellipsis: {
      tooltip: true,
    },
  },
  {
    title: t('workspace.resource.columns.format'),
    key: 'format',
    width: 100,
    render: (row) => h(NTag, { size: 'small' }, { default: () => row.format || '-' }),
  },
  {
    title: t('workspace.resource.columns.status'),
    key: 'status',
    width: 120,
    render: (row) =>
      h(
        NTag,
        { size: 'small', type: statusTagType(row.status) },
        { default: () => getResourceStatusLabel(row.status) },
      ),
  },
  {
    title: t('workspace.resource.columns.segments'),
    key: 'total_segments',
    width: 100,
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
    width: 300,
    fixed: 'right',
    render: (row) =>
      h(NSpace, { size: 4, wrap: false }, () => [
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            type: 'primary',
            onClick: () => void openSegmentsForResource(row),
          },
          { default: () => t('workspace.resource.actions.segments') },
        ),
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            loading: replacingResourceId.value === row.id,
            onClick: () => chooseReplacementFile(row.id),
          },
          { default: () => t('workspace.resource.actions.replace') },
        ),
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            loading: workspace.downloadingKeys.includes(`resource:${row.id}`),
            onClick: () => void downloadResource(row),
          },
          { default: () => t('workspace.common.download') },
        ),
        h(
          NPopconfirm,
          {
            positiveText: t('workspace.common.confirm'),
            negativeText: t('workspace.common.cancel'),
            onPositiveClick: () => void deleteResource(row),
          },
          {
            trigger: () =>
              h(
                NButton,
                {
                  size: 'small',
                  quaternary: true,
                  type: 'error',
                  loading: workspace.deletingResourceIds.includes(row.id),
                },
                { default: () => t('workspace.common.delete') },
              ),
            default: () => t('workspace.resource.deleteConfirm', { name: row.filename }),
          },
        ),
      ]),
  },
])

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
    ellipsis: {
      tooltip: true,
    },
  },
  {
    title: t('workspace.segment.columns.target'),
    key: 'target_text',
    minWidth: 260,
    ellipsis: {
      tooltip: true,
    },
    render: (row) =>
      row.target_text ||
      h(NText, { depth: 3 }, { default: () => t('workspace.segment.emptyTarget') }),
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
    width: 180,
    fixed: 'right',
    render: (row) =>
      h(NSpace, { size: 4, wrap: false }, () => [
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            type: 'primary',
            loading: workspace.editingSegmentIds.includes(row.id),
            onClick: () => openSegmentDrawer(row),
          },
          { default: () => t('workspace.segment.actions.edit') },
        ),
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            onClick: () => openSegmentJobDrawer(row),
          },
          { default: () => t('workspace.segment.actions.translate') },
        ),
      ]),
  },
])

const jobColumns = computed<DataTableColumns<TranslationJob>>(() => [
  {
    title: t('workspace.job.columns.id'),
    key: 'id',
    width: 90,
    render: (row) => `#${row.id}`,
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

watch(
  () => route.query.tab,
  (tab) => {
    if (tab === 'segments' || tab === 'jobs' || tab === 'resources') {
      activeTab.value = tab
    }
  },
  { immediate: true },
)

watch(
  () => [workspace.resourceSearch, workspace.resourceStatusFilter, workspace.resourceFormatFilter],
  () => {
    if (projectId.value) {
      void workspace.loadResources(projectId.value)
    }
  },
)

watch(
  () => [workspace.segmentSearch, workspace.segmentStatusFilter, workspace.activeResourceId],
  () => {
    if (projectId.value && workspace.activeResourceId) {
      void workspace.loadSegments(projectId.value, workspace.activeResourceId)
    }
  },
)

watch(
  () => workspace.jobStatusFilter,
  () => {
    if (projectId.value) {
      void workspace.loadJobs(projectId.value)
    }
  },
)

watch(activeTab, (tab) => {
  if (route.query.tab !== tab) {
    void router.replace({ query: { ...route.query, tab } })
  }
})

onMounted(() => {
  workspace.reset()
  void reloadWorkspace()
})

onBeforeUnmount(() => {
  for (const timer of uploadDismissTimers.values()) {
    clearTimeout(timer)
  }
  uploadDismissTimers.clear()
  workspace.reset()
})
</script>

<template>
  <div class="space-y-6">
    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-5 lg:flex-row lg:items-center lg:justify-between">
        <div class="min-w-0 space-y-3">
          <NButton quaternary size="small" @click="router.push('/projects')">
            {{ t('workspace.actions.back') }}
          </NButton>
          <div>
            <div class="text-xs font-medium uppercase text-brand-600">
              {{ t('workspace.eyebrow') }}
            </div>
            <h1 class="mt-2 truncate text-2xl font-semibold text-lf-text-strong">
              {{ workspace.project?.name || t('workspace.loadingProject') }}
            </h1>
            <div class="mt-2 flex flex-wrap items-center gap-2 text-sm text-lf-text-muted">
              <span>{{ t('workspace.projectId', { id: projectId ?? '-' }) }}</span>
              <span>·</span>
              <span
                >{{ workspace.project?.source_lang || '-' }} →
                {{ workspace.project?.target_lang || '-' }}</span
              >
              <span>·</span>
              <span>{{
                t('workspace.updatedAt', {
                  time: formatDate(workspace.project?.updated_at ?? workspace.project?.created_at),
                })
              }}</span>
            </div>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton
            secondary
            :loading="
              workspace.loadingProject || workspace.loadingResources || workspace.loadingJobs
            "
            @click="reloadWorkspace"
          >
            {{ t('workspace.actions.refresh') }}
          </NButton>
          <NButton type="primary" :disabled="!canCreateResourceJob" @click="openResourceJobDrawer">
            {{ t('workspace.job.actions.createFromResources') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <NAlert v-if="workspace.projectError" type="error" :bordered="false">
      {{ workspace.projectError }}
    </NAlert>

    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs text-lf-text-muted">{{ t('workspace.stats.resources') }}</div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ workspace.resources.length }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs text-lf-text-muted">{{ t('workspace.stats.readyResources') }}</div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ workspace.readyResourceCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs text-lf-text-muted">{{ t('workspace.stats.segments') }}</div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ workspace.totalSegmentCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs text-lf-text-muted">{{ t('workspace.stats.runningJobs') }}</div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ workspace.runningJobCount }}
        </div>
      </NCard>
    </div>

    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <NTabs v-model:value="activeTab" animated>
        <NTabPane name="resources" :tab="t('workspace.tabs.resources')">
          <div class="space-y-4 pt-2">
            <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
              <div class="flex flex-1 flex-col gap-3 md:flex-row">
                <NInput
                  v-model:value="workspace.resourceSearch"
                  clearable
                  class="md:max-w-sm"
                  :placeholder="t('workspace.resource.searchPlaceholder')"
                />
                <NSelect
                  v-model:value="workspace.resourceStatusFilter"
                  class="md:w-44"
                  :options="resourceStatusOptions"
                />
                <NSelect
                  v-model:value="workspace.resourceFormatFilter"
                  class="md:w-44"
                  :options="resourceFormatOptions"
                />
              </div>
              <div class="flex flex-wrap gap-3">
                <NUpload multiple :show-file-list="false" :custom-request="handleUpload">
                  <NButton type="primary" :loading="workspace.hasActiveUploads">
                    {{ t('workspace.resource.actions.upload') }}
                  </NButton>
                </NUpload>
                <NButton :disabled="!canCreateResourceJob" @click="openResourceJobDrawer">
                  {{ t('workspace.job.actions.create') }}
                </NButton>
              </div>
            </div>

            <TransitionGroup
              enter-active-class="transition-all duration-300 ease-out"
              leave-active-class="transition-all duration-200 ease-in"
              enter-from-class="opacity-0 -translate-y-2"
              leave-to-class="opacity-0 -translate-y-2"
              move-class="transition-transform duration-200"
              tag="div"
              class="space-y-2"
            >
              <div
                v-for="task in workspace.uploadTasks"
                :key="task.id"
                class="overflow-hidden rounded-lg border border-lf-border bg-lf-surface-muted px-4 py-3"
              >
                <div class="flex items-center justify-between">
                  <div class="flex min-w-0 flex-1 items-center gap-2.5">
                    <div
                      class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md"
                      :class="{
                        'bg-blue-50 text-blue-500 dark:bg-blue-500/10': task.stage === 'uploading',
                        'bg-amber-50 text-amber-500 dark:bg-amber-500/10':
                          task.stage === 'processing',
                        'bg-emerald-50 text-emerald-500 dark:bg-emerald-500/10':
                          task.stage === 'complete',
                        'bg-red-50 text-red-500 dark:bg-red-500/10': task.stage === 'error',
                      }"
                    >
                      <IconLucideUpload v-if="task.stage === 'uploading'" class="h-3.5 w-3.5" />
                      <IconLucideLoader2
                        v-else-if="task.stage === 'processing'"
                        class="h-3.5 w-3.5 animate-spin"
                      />
                      <IconLucideCheck v-else-if="task.stage === 'complete'" class="h-3.5 w-3.5" />
                      <IconLucideAlertCircle v-else class="h-3.5 w-3.5" />
                    </div>
                    <div class="min-w-0 flex-1">
                      <div class="flex items-baseline justify-between gap-2">
                        <span class="truncate text-sm font-medium text-lf-text-strong">
                          {{ task.fileName }}
                        </span>
                        <span class="shrink-0 text-xs text-lf-text-muted">
                          <template v-if="task.stage === 'uploading'">
                            {{ t('workspace.upload.uploadingPercent', { percent: task.progress }) }}
                          </template>
                          <template v-else-if="task.stage === 'processing'">
                            {{ t('workspace.upload.processing') }}
                          </template>
                          <template v-else-if="task.stage === 'complete'">
                            {{ t('workspace.upload.complete') }}
                          </template>
                          <template v-else>
                            {{ task.errorMessage || t('workspace.upload.failed') }}
                          </template>
                        </span>
                      </div>
                      <NProgress
                        class="mt-1.5"
                        type="line"
                        :percentage="task.progress"
                        :status="
                          task.stage === 'complete'
                            ? 'success'
                            : task.stage === 'error'
                              ? 'error'
                              : undefined
                        "
                        :show-indicator="false"
                        :processing="task.stage === 'uploading' || task.stage === 'processing'"
                      />
                    </div>
                  </div>
                  <NButton
                    quaternary
                    size="tiny"
                    class="ml-2 shrink-0"
                    @click="dismissTask(task.id)"
                  >
                    <template #icon>
                      <IconLucideX class="h-3.5 w-3.5" />
                    </template>
                  </NButton>
                </div>
              </div>
            </TransitionGroup>

            <NAlert v-if="workspace.resourcesError" type="error" :bordered="false">
              {{ workspace.resourcesError }}
            </NAlert>

            <NDataTable
              v-model:checked-row-keys="workspace.selectedResourceIds"
              remote
              :columns="resourceColumns"
              :data="workspace.resources"
              :loading="workspace.loadingResources"
              :row-key="(row: Resource) => row.id"
              :scroll-x="1120"
            >
              <template #empty>
                <NEmpty
                  v-if="!workspace.loadingResources && workspace.resources.length === 0"
                  :description="t('workspace.resource.empty')"
                >
                  <template #extra>
                    <NUpload multiple :show-file-list="false" :custom-request="handleUpload">
                      <NButton type="primary">{{
                        t('workspace.resource.actions.uploadFirst')
                      }}</NButton>
                    </NUpload>
                  </template>
                </NEmpty>
              </template>
            </NDataTable>
          </div>
        </NTabPane>

        <NTabPane name="segments" :tab="t('workspace.tabs.segments')">
          <div class="space-y-4 pt-2">
            <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
              <div class="flex flex-1 flex-col gap-3 md:flex-row">
                <NSelect
                  v-model:value="workspace.activeResourceId"
                  clearable
                  class="md:max-w-sm"
                  :options="
                    workspace.resources.map((resource) => ({
                      label: resource.filename,
                      value: resource.id,
                    }))
                  "
                  :placeholder="t('workspace.segment.resourcePlaceholder')"
                  @update:value="(value: number | null) => workspace.setActiveResource(value)"
                />
                <NInput
                  v-model:value="workspace.segmentSearch"
                  clearable
                  class="md:max-w-sm"
                  :disabled="!workspace.activeResourceId"
                  :placeholder="t('workspace.segment.searchPlaceholder')"
                />
                <NSelect
                  v-model:value="workspace.segmentStatusFilter"
                  class="md:w-44"
                  :disabled="!workspace.activeResourceId"
                  :options="segmentStatusOptions"
                />
              </div>
              <div class="flex flex-wrap gap-3">
                <NButton
                  secondary
                  :disabled="!workspace.activeResourceId"
                  :loading="workspace.loadingSegments"
                  @click="reloadSegments"
                >
                  {{ t('workspace.actions.refresh') }}
                </NButton>
                <NButton
                  type="primary"
                  :disabled="!canCreateSegmentJob"
                  @click="openSegmentJobDrawer()"
                >
                  {{ t('workspace.job.actions.createFromSegments') }}
                </NButton>
              </div>
            </div>

            <NAlert v-if="workspace.segmentsError" type="error" :bordered="false">
              {{ workspace.segmentsError }}
            </NAlert>

            <NEmpty
              v-if="!workspace.activeResourceId"
              class="py-12"
              :description="t('workspace.segment.noResource')"
            />
            <template v-else>
              <NDataTable
                remote
                :columns="segmentColumns"
                :data="workspace.segments"
                :loading="workspace.loadingSegments"
                :row-key="(row: Segment) => row.id"
                :scroll-x="1040"
              />
              <div v-if="workspace.segmentsCursor" class="flex justify-center pt-3">
                <NButton
                  :loading="workspace.loadingSegments"
                  @click="workspace.loadSegments(projectId!, workspace.activeResourceId!, true)"
                >
                  {{ t('common.loadMore') }}
                </NButton>
              </div>
              <NEmpty
                v-if="!workspace.loadingSegments && workspace.segments.length === 0"
                class="py-12"
                :description="t('workspace.segment.empty')"
              />
            </template>
          </div>
        </NTabPane>

        <NTabPane name="jobs" :tab="t('workspace.tabs.jobs')">
          <div class="space-y-4 pt-2">
            <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
              <NSelect
                v-model:value="workspace.jobStatusFilter"
                class="md:w-56"
                :options="jobStatusOptions"
              />
              <div class="flex flex-wrap gap-3">
                <NButton
                  secondary
                  :loading="workspace.loadingJobs"
                  @click="projectId && workspace.loadJobs(projectId)"
                >
                  {{ t('workspace.actions.refresh') }}
                </NButton>
                <NButton
                  type="primary"
                  :disabled="!canCreateResourceJob"
                  @click="openResourceJobDrawer"
                >
                  {{ t('workspace.job.actions.create') }}
                </NButton>
              </div>
            </div>

            <NAlert v-if="workspace.jobsError" type="error" :bordered="false">
              {{ workspace.jobsError }}
            </NAlert>

            <NDataTable
              remote
              :columns="jobColumns"
              :data="workspace.jobs"
              :loading="workspace.loadingJobs"
              :row-key="(row: TranslationJob) => row.id"
              :row-props="
                (row: TranslationJob) => ({
                  class: 'cursor-pointer',
                  onClick: () => openJobDetail(row),
                })
              "
              :scroll-x="1320"
            />
            <div v-if="workspace.jobsCursor" class="flex justify-center pt-3">
              <NButton
                :loading="workspace.loadingJobs"
                @click="workspace.loadJobs(projectId!, true)"
              >
                {{ t('common.loadMore') }}
              </NButton>
            </div>
            <NEmpty
              v-if="!workspace.loadingJobs && workspace.jobs.length === 0"
              class="py-12"
              :description="t('workspace.job.empty')"
            />
          </div>
        </NTabPane>
      </NTabs>
    </NCard>

    <NDrawer v-model:show="segmentDrawerVisible" :width="620" placement="right">
      <NDrawerContent :title="t('workspace.segment.editTitle')" closable>
        <NForm :model="segmentForm" label-placement="top">
          <NFormItem :label="t('workspace.segment.form.source')">
            <NInput
              v-model:value="segmentForm.source_text"
              type="textarea"
              :autosize="{ minRows: 5, maxRows: 12 }"
            />
          </NFormItem>
          <NFormItem :label="t('workspace.segment.form.target')">
            <NInput
              v-model:value="segmentForm.target_text"
              type="textarea"
              :autosize="{ minRows: 5, maxRows: 12 }"
            />
          </NFormItem>
          <NFormItem :label="t('workspace.segment.form.comment')">
            <NInput
              v-model:value="segmentForm.comment"
              type="textarea"
              :autosize="{ minRows: 2, maxRows: 5 }"
            />
          </NFormItem>
        </NForm>
        <template #footer>
          <div class="flex flex-wrap justify-end gap-3">
            <NButton @click="closeSegmentDrawer">{{ t('workspace.common.cancel') }}</NButton>
            <NButton
              :disabled="!editingSegment"
              @click="editingSegment && openSegmentJobDrawer(editingSegment)"
            >
              {{ t('workspace.segment.actions.translate') }}
            </NButton>
            <NButton
              type="primary"
              :loading="
                Boolean(editingSegment && workspace.editingSegmentIds.includes(editingSegment.id))
              "
              @click="saveSegment"
            >
              {{ t('workspace.common.save') }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>

    <NDrawer v-model:show="jobDrawerVisible" :width="520" placement="right">
      <NDrawerContent :title="t('workspace.job.createTitle')" closable>
        <div class="mb-5 rounded-lg bg-lf-surface-muted p-4 text-sm text-lf-text-muted">
          {{
            jobTargetMode === 'resources'
              ? t('workspace.job.targetResources', { count: jobTargetResourceIds.length })
              : t('workspace.job.targetSegments', { count: jobTargetSegmentIds.length })
          }}
        </div>
        <NForm :model="jobForm" label-placement="top">
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <NFormItem :label="t('workspace.job.form.sourceLang')">
              <NInput v-model:value="jobForm.source_lang" />
            </NFormItem>
            <NFormItem :label="t('workspace.job.form.targetLang')">
              <NInput v-model:value="jobForm.target_lang" />
            </NFormItem>
          </div>
          <NFormItem :label="t('workspace.job.form.backendOrder')">
            <NInput
              v-model:value="jobForm.backend_order_text"
              type="textarea"
              :autosize="{ minRows: 4, maxRows: 8 }"
              :placeholder="t('workspace.job.form.backendOrderPlaceholder')"
            />
          </NFormItem>
        </NForm>
        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton :disabled="workspace.creatingJob" @click="closeJobDrawer">{{
              t('workspace.common.cancel')
            }}</NButton>
            <NButton type="primary" :loading="workspace.creatingJob" @click="submitJob">
              {{ t('workspace.job.actions.submitCreate') }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>

    <NDrawer v-model:show="jobDetailDrawerVisible" :width="720" placement="right">
      <NDrawerContent
        :title="
          workspace.selectedJob
            ? t('workspace.job.detailTitle', { id: workspace.selectedJob.id })
            : t('workspace.job.detailFallbackTitle')
        "
        closable
      >
        <NSpin :show="workspace.loadingJobDetail">
          <div v-if="workspace.selectedJob" class="space-y-5">
            <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <div class="rounded-lg bg-lf-surface-muted p-4">
                <div class="text-xs text-lf-text-muted">
                  {{ t('workspace.job.columns.status') }}
                </div>
                <NTag class="mt-2" size="small" :type="statusTagType(workspace.selectedJob.status)">
                  {{ getJobStatusLabel(workspace.selectedJob.status) }}
                </NTag>
              </div>
              <div class="rounded-lg bg-lf-surface-muted p-4">
                <div class="text-xs text-lf-text-muted">
                  {{ t('workspace.job.columns.resources') }}
                </div>
                <div class="mt-2 text-lg font-semibold text-lf-text-strong">
                  {{ workspace.selectedJob.completed_resources }}/{{
                    workspace.selectedJob.resource_count
                  }}
                </div>
              </div>
              <div class="rounded-lg bg-lf-surface-muted p-4">
                <div class="text-xs text-lf-text-muted">
                  {{ t('workspace.job.columns.segments') }}
                </div>
                <div class="mt-2 text-lg font-semibold text-lf-text-strong">
                  {{ workspace.selectedJob.completed_segments }}/{{
                    workspace.selectedJob.total_segments
                  }}
                </div>
              </div>
            </div>

            <NProgress
              type="line"
              :percentage="getJobProgress(workspace.selectedJob)"
              indicator-placement="inside"
              :processing="
                workspace.selectedJob.status === 'pending' ||
                workspace.selectedJob.status === 'running'
              "
            />

            <NAlert v-if="workspace.jobDetailError" type="error" :bordered="false">
              {{ workspace.jobDetailError }}
            </NAlert>
            <NAlert v-if="workspace.selectedJob.error_message" type="error" :bordered="false">
              {{ workspace.selectedJob.error_message }}
            </NAlert>

            <NDescriptions bordered :column="1" size="small">
              <NDescriptionsItem :label="t('workspace.job.columns.trigger')">
                {{ getJobTriggerLabel(workspace.selectedJob.trigger_type) }}
              </NDescriptionsItem>
              <NDescriptionsItem :label="t('workspace.common.createdAt')">
                {{ formatDate(workspace.selectedJob.created_at) }}
              </NDescriptionsItem>
              <NDescriptionsItem :label="t('workspace.common.updatedAt')">
                {{ formatDate(workspace.selectedJob.updated_at) }}
              </NDescriptionsItem>
              <NDescriptionsItem :label="t('workspace.job.form.sourceLang')">
                {{ formatConfigValue(workspace.selectedJob.translation_config?.source_lang) }}
              </NDescriptionsItem>
              <NDescriptionsItem :label="t('workspace.job.form.targetLang')">
                {{ formatConfigValue(workspace.selectedJob.translation_config?.target_lang) }}
              </NDescriptionsItem>
              <NDescriptionsItem :label="t('workspace.job.form.backendOrder')">
                <pre class="m-0 whitespace-pre-wrap text-xs leading-5">{{
                  formatConfigValue(workspace.selectedJob.translation_config?.backend_order)
                }}</pre>
              </NDescriptionsItem>
            </NDescriptions>

            <div>
              <div class="mb-3 text-sm font-medium text-lf-text-strong">
                {{ t('workspace.job.resourcesTitle') }}
              </div>
              <NDataTable
                :data="workspace.selectedJob.job_resources ?? []"
                :columns="[
                  {
                    title: t('workspace.resource.columns.filename'),
                    key: 'filename',
                    render: (row: ApiSchemas['TranslationJobResource']) =>
                      row.resource?.filename || `#${row.resource_id}`,
                  },
                  {
                    title: t('workspace.job.columns.status'),
                    key: 'status',
                    render: (row: ApiSchemas['TranslationJobResource']) =>
                      getJobStatusLabel(row.status as TranslationJob['status']),
                  },
                  {
                    title: t('workspace.job.columns.segments'),
                    key: 'segments',
                    render: (row: ApiSchemas['TranslationJobResource']) =>
                      `${row.completed_segments}/${row.segment_count}`,
                  },
                  {
                    title: t('workspace.job.columns.error'),
                    key: 'error_message',
                    render: (row: ApiSchemas['TranslationJobResource']) => row.error_message || '-',
                  },
                ]"
                :row-key="(row: ApiSchemas['TranslationJobResource']) => row.id"
                :scroll-x="720"
              />
            </div>
          </div>
          <NEmpty v-else :description="t('workspace.job.detailEmpty')" />
        </NSpin>
        <template #footer>
          <div class="flex flex-wrap justify-end gap-3">
            <NButton @click="jobDetailDrawerVisible = false">{{
              t('workspace.common.close')
            }}</NButton>
            <NButton
              v-if="workspace.selectedJob"
              :disabled="
                workspace.selectedJob.status !== 'completed' &&
                workspace.selectedJob.status !== 'awaiting_review'
              "
              :loading="workspace.downloadingKeys.includes(`job:${workspace.selectedJob.id}:all`)"
              type="primary"
              @click="downloadJob(workspace.selectedJob)"
            >
              {{ t('workspace.common.download') }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>
