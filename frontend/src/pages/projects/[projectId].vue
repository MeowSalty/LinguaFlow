<script setup lang="ts">
import {
  NAlert,
  NButton,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NModal,
  NPopover,
  NProgress,
  NSpace,
  NTag,
  NText,
  NUpload,
  useMessage,
  type DataTableColumns,
  type FormInst,
  type FormRules,
  type SelectOption,
  type UploadFileInfo,
} from 'naive-ui'
import { h } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas, type DownloadFileResult } from '@/api/client'
import ResourceExplorer from '@/components/workspace/ResourceExplorer.vue'
import WorkspaceMetricsBar from '@/components/workspace/WorkspaceMetricsBar.vue'
import { useGlossaryStore } from '@/stores/glossary'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type Resource = ApiSchemas['Resource']
type Segment = ApiSchemas['Segment']
type TranslationJob = ApiSchemas['TranslationJob']
type CreateTranslationJobPayload = ApiSchemas['CreateTranslationJobRequest']
type IncrementalUpdateResponse = ApiSchemas['IncrementalUpdateResponse']

type WorkspaceTab = 'resources' | 'segments' | 'jobs' | 'glossary'
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
const glossary = useGlossaryStore()

const activeTab = ref<WorkspaceTab>('resources')

// ── 术语表状态 ──
const glossaryDrawerVisible = ref(false)
const editingGlossaryEntry = ref<ApiSchemas['GlossaryEntry'] | null>(null)
const glossaryFormRef = ref<FormInst | null>(null)
const glossaryImportVisible = ref(false)

interface GlossaryFormModel {
  source: string
  target: string
  case_sensitive: boolean
  notes: string
}

const glossaryForm = reactive<GlossaryFormModel>({
  source: '',
  target: '',
  case_sensitive: false,
  notes: '',
})

const isGlossaryEditMode = computed(() => Boolean(editingGlossaryEntry.value))
const glossaryDrawerTitle = computed(() =>
  isGlossaryEditMode.value
    ? t('workspace.segment.editTitle')
    : t('workspace.glossary.actions.create'),
)

const glossaryRules = computed<FormRules>(() => ({
  source: [
    {
      required: true,
      message: t('workspace.glossary.validation.sourceRequired'),
      trigger: ['input', 'blur'],
    },
  ],
  target: [
    {
      required: true,
      message: t('workspace.glossary.validation.targetRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

const resetGlossaryForm = (): void => {
  glossaryForm.source = ''
  glossaryForm.target = ''
  glossaryForm.case_sensitive = false
  glossaryForm.notes = ''
}

const openCreateGlossaryDrawer = (): void => {
  editingGlossaryEntry.value = null
  resetGlossaryForm()
  glossaryDrawerVisible.value = true
}

const openEditGlossaryDrawer = (entry: ApiSchemas['GlossaryEntry']): void => {
  editingGlossaryEntry.value = entry
  glossaryForm.source = entry.source
  glossaryForm.target = entry.target
  glossaryForm.case_sensitive = entry.case_sensitive
  glossaryForm.notes = entry.notes ?? ''
  glossaryDrawerVisible.value = true
}

const closeGlossaryDrawer = (): void => {
  glossaryDrawerVisible.value = false
  editingGlossaryEntry.value = null
  resetGlossaryForm()
}

const submitGlossaryEntry = async (): Promise<void> => {
  await glossaryFormRef.value?.validate()

  if (!projectId.value) {
    return
  }

  try {
    if (editingGlossaryEntry.value) {
      await glossary.updateEntry(projectId.value, editingGlossaryEntry.value.id, {
        source: glossaryForm.source.trim(),
        target: glossaryForm.target.trim(),
        case_sensitive: glossaryForm.case_sensitive,
        notes: glossaryForm.notes.trim() || undefined,
      })
      message.success(t('workspace.glossary.messages.updateSuccess'))
    } else {
      await glossary.createEntry(projectId.value, {
        source: glossaryForm.source.trim(),
        target: glossaryForm.target.trim(),
        case_sensitive: glossaryForm.case_sensitive,
        notes: glossaryForm.notes.trim() || undefined,
      })
      message.success(t('workspace.glossary.messages.createSuccess'))
    }

    closeGlossaryDrawer()
  } catch (error) {
    console.error(error)
    message.error(
      editingGlossaryEntry.value
        ? glossary.updateError || t('workspace.glossary.messages.updateFailed')
        : glossary.createError || t('workspace.glossary.messages.createFailed'),
    )
  }
}

const deleteGlossaryEntry = async (entry: ApiSchemas['GlossaryEntry']): Promise<void> => {
  if (!projectId.value) {
    return
  }

  try {
    await glossary.deleteEntry(projectId.value, entry.id)
    message.success(t('workspace.glossary.messages.deleteSuccess'))
  } catch (error) {
    console.error(error)
    message.error(glossary.deleteError || t('workspace.glossary.messages.deleteFailed'))
  }
}

const handleGlossaryImport = async (file: File): Promise<void> => {
  if (!projectId.value) {
    return
  }

  try {
    const result = await glossary.importCSV(projectId.value, file)
    message.success(
      t('workspace.glossary.import.result', { added: result.added }) +
        (result.skipped?.length
          ? `，${t('workspace.glossary.import.skipped', { count: result.skipped.length })}`
          : ''),
    )
    glossaryImportVisible.value = false
  } catch (error) {
    console.error(error)
    message.error(glossary.importError || t('workspace.glossary.messages.importFailed'))
  }
}

const handleGlossaryExport = async (): Promise<void> => {
  if (!projectId.value) {
    return
  }

  try {
    await glossary.exportCSV(projectId.value)
  } catch (error) {
    console.error(error)
    message.error(t('workspace.glossary.messages.exportFailed'))
  }
}

const glossaryColumns = computed<DataTableColumns<ApiSchemas['GlossaryEntry']>>(() => [
  {
    title: '#',
    key: 'id',
    width: 64,
    render: (_row, index) => `${index + 1}`,
  },
  {
    title: t('workspace.glossary.columns.source'),
    key: 'source',
    minWidth: 180,
    ellipsis: { tooltip: true },
  },
  {
    title: t('workspace.glossary.columns.target'),
    key: 'target',
    minWidth: 180,
    ellipsis: { tooltip: true },
  },
  {
    title: t('workspace.glossary.columns.caseSensitive'),
    key: 'case_sensitive',
    width: 120,
    render: (row) =>
      row.case_sensitive
        ? h(
            NTag,
            { size: 'small', type: 'info', bordered: false },
            { default: () => t('workspace.segment.status.reviewed') },
          )
        : h(NText, { depth: 3 }, { default: () => '—' }),
  },
  {
    title: t('workspace.glossary.columns.notes'),
    key: 'notes',
    minWidth: 160,
    ellipsis: { tooltip: true },
    render: (row) => row.notes || h(NText, { depth: 3 }, { default: () => '—' }),
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
    width: 160,
    fixed: 'right',
    render: (row) =>
      h(NSpace, { size: 4, wrap: false }, () => [
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            type: 'primary',
            onClick: () => openEditGlossaryDrawer(row),
          },
          { default: () => t('workspace.segment.actions.edit') },
        ),
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            type: 'error',
            loading: glossary.deletingEntryIds.includes(row.id),
            onClick: () => deleteGlossaryEntry(row),
          },
          { default: () => t('workspace.common.delete') },
        ),
      ]),
  },
])
const jobDrawerVisible = ref(false)
const jobDetailDrawerVisible = ref(false)
const jobTargetMode = ref<JobTargetMode>('resources')
const jobTargetResourceIds = ref<number[]>([])
const jobTargetSegmentIds = ref<number[]>([])
const conflictDialogVisible = ref(false)
const conflictResource = ref<Resource | null>(null)
const conflictFile = ref<File | null>(null)
const replacingResourceId = ref<number | null>(null)
const incrementalResultVisible = ref(false)
const incrementalResult = ref<IncrementalUpdateResponse | null>(null)

// ── 内联编辑状态 ──
const inlineEditingSegmentId = ref<number | null>(null)
const inlineEditForm = reactive<SegmentFormModel>({
  source_text: '',
  target_text: '',
  comment: '',
})
const inlineCommentVisible = ref<number | null>(null)
const inlineCommentText = ref('')

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
const canCreateSegmentJob = computed(() => workspace.segments.length > 0)

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
    workspace.loadResourceTree(projectId.value),
    workspace.loadResources(projectId.value),
    workspace.loadJobs(projectId.value),
    glossary.loadEntries(projectId.value),
  ])
  workspace.syncResourcesFromTree()
}

const reloadSegments = async (): Promise<void> => {
  if (!projectId.value || !workspace.activeResourceId) {
    return
  }

  await workspace.loadSegments(projectId.value, workspace.activeResourceId)
}

// ── ResourceExplorer 事件处理 ──

const handleExplorerOpenSegments = (resource: Resource): void => {
  workspace.setActiveResource(resource.id)
  activeTab.value = 'segments'
  void reloadSegments()
}

const handleExplorerConflict = (resource: Resource, file: File): void => {
  conflictResource.value = resource
  conflictFile.value = file
  conflictDialogVisible.value = true
}

const handleExplorerIncrementalResult = (result: IncrementalUpdateResponse): void => {
  incrementalResult.value = result
  incrementalResultVisible.value = true
}

const resetConflictState = (): void => {
  conflictResource.value = null
  conflictFile.value = null
}

const handleConflictReplace = async (): Promise<void> => {
  if (!projectId.value || !conflictResource.value || !conflictFile.value) {
    return
  }

  conflictDialogVisible.value = false
  const resourceId = conflictResource.value.id
  const file = conflictFile.value
  resetConflictState()

  replacingResourceId.value = resourceId
  try {
    await workspace.replaceResource(projectId.value, resourceId, file)
    message.success(t('workspace.messages.replaceSuccess'))
    await workspace.loadResourceTree(projectId.value)
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

const handleConflictIncremental = async (): Promise<void> => {
  if (!projectId.value || !conflictResource.value || !conflictFile.value) {
    return
  }

  conflictDialogVisible.value = false
  const resourceId = conflictResource.value.id
  const file = conflictFile.value
  resetConflictState()

  try {
    const result = await workspace.incrementalUpdateResource(projectId.value, resourceId, file)
    incrementalResult.value = result
    incrementalResultVisible.value = true
    await workspace.loadResourceTree(projectId.value)
    if (workspace.activeResourceId === resourceId) {
      await reloadSegments()
    }
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.incrementalUpdateFailed'))
  }
}

const confirmIncrementalResult = (): void => {
  incrementalResultVisible.value = false
  incrementalResult.value = null
}

// ── 段落操作 ──

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
  if (!projectId.value || !workspace.activeResourceId) {
    return
  }

  try {
    await workspace.updateSegment(projectId.value, workspace.activeResourceId, segment.id, {
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
  if (!projectId.value || !workspace.activeResourceId) {
    return
  }

  try {
    await workspace.updateSegment(projectId.value, workspace.activeResourceId, segment.id, {
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

// ── 翻译任务操作 ──

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
            onClick: () => openSegmentJobDrawer(row),
          },
          { default: () => t('workspace.segment.actions.translate') },
        ),
      ])
    },
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

// ── Watchers ──

watch(
  () => route.query.tab,
  (tab) => {
    if (tab === 'segments' || tab === 'jobs' || tab === 'resources' || tab === 'glossary') {
      activeTab.value = tab
    }
  },
  { immediate: true },
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
  glossary.reset()
  void reloadWorkspace()
})

onBeforeUnmount(() => {
  workspace.reset()
  glossary.reset()
})
</script>

<template>
  <div class="space-y-6">
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-lf-shadow">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
          <NButton quaternary size="small" @click="router.push('/projects')">
            <template #icon>
              <NIcon><IconCarbonArrowLeft /></NIcon>
            </template>
          </NButton>
          <h1 class="truncate text-lg font-semibold tracking-tight text-lf-text-strong">
            {{ workspace.project?.name || t('workspace.loadingProject') }}
          </h1>
          <span class="inline-block h-4 w-px bg-lf-border-soft" />
          <span class="inline-flex items-center gap-1.5 text-sm text-lf-text-muted">
            <IconCarbonLanguage class="h-3.5 w-3.5 text-lf-text-subtle" />
            {{ workspace.project?.source_lang || '-' }} →
            {{ workspace.project?.target_lang || '-' }}
          </span>
          <span class="hidden h-4 w-px bg-lf-border-soft md:inline-block" />
          <span class="hidden items-center gap-1.5 text-sm text-lf-text-muted md:inline-flex">
            <IconCarbonTime class="h-3.5 w-3.5 text-lf-text-subtle" />
            {{
              t('workspace.updatedAt', {
                time: formatDate(workspace.project?.updated_at ?? workspace.project?.created_at),
              })
            }}
          </span>
        </div>
        <div class="flex shrink-0 flex-wrap gap-3">
          <NButton
            secondary
            :loading="
              workspace.loadingProject || workspace.loadingResourceTree || workspace.loadingJobs
            "
            @click="reloadWorkspace"
          >
            <template #icon>
              <NIcon><IconCarbonRenew /></NIcon>
            </template>
            {{ t('workspace.actions.refresh') }}
          </NButton>
          <NButton type="primary" :disabled="!canCreateResourceJob" @click="openResourceJobDrawer">
            <template #icon>
              <NIcon><IconCarbonMagicWand /></NIcon>
            </template>
            {{ t('workspace.job.actions.createFromResources') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <NAlert v-if="workspace.projectError" type="error" :bordered="false">
      {{ workspace.projectError }}
    </NAlert>

    <WorkspaceMetricsBar
      :total-resources="workspace.resources.length"
      :ready-resources="workspace.readyResourceCount"
      :total-segments="workspace.totalSegmentCount"
      :translated-segments="workspace.translatedSegmentCount"
      :running-jobs="workspace.runningJobCount"
    />

    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div
        v-if="activeTab === 'resources' && selectedReadyResourceIds.length > 0"
        class="mb-3 inline-flex items-center gap-2 rounded-full bg-lf-surface-muted px-3 py-1.5 text-xs text-lf-text-muted"
      >
        <IconCarbonSelect-01 class="h-3.5 w-3.5" />
        {{ t('workspace.content.selectedResources', { count: selectedReadyResourceIds.length }) }}
      </div>

      <NTabs v-model:value="activeTab" animated>
        <NTabPane
          name="resources"
          :tab="`${t('workspace.tabs.resources')} (${workspace.resources.length})`"
        >
          <div class="pt-3">
            <ResourceExplorer
              v-if="projectId"
              :project-id="projectId"
              @open-segments="handleExplorerOpenSegments"
              @conflict="handleExplorerConflict"
              @incremental-result="handleExplorerIncrementalResult"
            />
          </div>
        </NTabPane>

        <NTabPane
          name="segments"
          :tab="`${t('workspace.tabs.segments')} (${workspace.totalSegmentCount})`"
        >
          <div class="space-y-4 pt-3">
            <div class="rounded-xl border border-lf-border-soft bg-lf-surface-muted/60 p-4">
              <div class="mb-4 flex flex-col gap-1">
                <h3 class="text-base font-semibold text-lf-text-strong">
                  {{ t('workspace.sections.segments.title') }}
                </h3>
                <p class="text-sm text-lf-text-muted">
                  {{ t('workspace.sections.segments.description') }}
                </p>
              </div>
              <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
                <div class="flex flex-1 flex-col gap-3 md:flex-row">
                  <NSelect
                    v-model:value="workspace.activeResourceId"
                    clearable
                    class="md:max-w-sm"
                    :options="
                      workspace.resources.map((resource) => ({
                        label: resource.path,
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

        <NTabPane name="jobs" :tab="`${t('workspace.tabs.jobs')} (${workspace.jobs.length})`">
          <div class="space-y-4 pt-3">
            <div class="rounded-xl border border-lf-border-soft bg-lf-surface-muted/60 p-4">
              <div class="mb-4 flex flex-col gap-1">
                <h3 class="text-base font-semibold text-lf-text-strong">
                  {{ t('workspace.sections.jobs.title') }}
                </h3>
                <p class="text-sm text-lf-text-muted">
                  {{ t('workspace.sections.jobs.description') }}
                </p>
              </div>
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

        <NTabPane name="glossary" :tab="`${t('workspace.tabs.glossary')} (${glossary.entryCount})`">
          <div class="space-y-4 pt-3">
            <div class="rounded-xl border border-lf-border-soft bg-lf-surface-muted/60 p-4">
              <div class="mb-4 flex flex-col gap-1">
                <h3 class="text-base font-semibold text-lf-text-strong">
                  {{ t('workspace.sections.glossary.title') }}
                </h3>
                <p class="text-sm text-lf-text-muted">
                  {{ t('workspace.sections.glossary.description') }}
                </p>
              </div>
              <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
                <NInput
                  v-model:value="glossary.searchQuery"
                  clearable
                  class="md:max-w-sm"
                  :placeholder="t('workspace.segment.searchPlaceholder')"
                />
                <div class="flex gap-2">
                  <NButton
                    secondary
                    :loading="glossary.importing"
                    @click="glossaryImportVisible = true"
                  >
                    <template #icon>
                      <NIcon><IconCarbonUpload /></NIcon>
                    </template>
                    {{ t('workspace.glossary.actions.import') }}
                  </NButton>
                  <NButton secondary @click="handleGlossaryExport">
                    <template #icon>
                      <NIcon><IconCarbonDownload /></NIcon>
                    </template>
                    {{ t('workspace.glossary.actions.export') }}
                  </NButton>
                  <NButton type="primary" @click="openCreateGlossaryDrawer">
                    <template #icon>
                      <NIcon><IconCarbonAdd /></NIcon>
                    </template>
                    {{ t('workspace.glossary.actions.create') }}
                  </NButton>
                </div>
              </div>
            </div>

            <NAlert v-if="glossary.error" type="error" :bordered="false">
              {{ glossary.error }}
            </NAlert>

            <NAlert v-if="glossary.importError" type="error" :bordered="false">
              {{ glossary.importError }}
            </NAlert>

            <NDataTable
              :columns="glossaryColumns"
              :data="glossary.filteredItems"
              :loading="glossary.loading"
              :row-key="(row: ApiSchemas['GlossaryEntry']) => row.id"
              :scroll-x="960"
            >
              <template #empty>
                <NEmpty
                  v-if="!glossary.loading && glossary.filteredItems.length === 0"
                  class="py-12"
                  :description="
                    glossary.searchQuery.trim()
                      ? t('workspace.glossary.empty.filtered')
                      : t('workspace.glossary.empty.default')
                  "
                />
              </template>
            </NDataTable>
          </div>
        </NTabPane>
      </NTabs>
    </NCard>

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
                    title: t('workspace.resource.columns.name'),
                    key: 'name',
                    render: (row: ApiSchemas['TranslationJobResource']) =>
                      row.resource?.name || `#${row.resource_id}`,
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

    <NModal
      v-model:show="conflictDialogVisible"
      preset="card"
      :title="t('workspace.conflict.title')"
      :style="{ width: '440px' }"
      :bordered="false"
      :mask-closable="false"
    >
      <div class="space-y-3">
        <NAlert type="warning" :bordered="false">
          {{ t('workspace.conflict.description', { name: conflictResource?.name ?? '' }) }}
        </NAlert>
        <p class="text-sm text-lf-text-muted">
          {{ t('workspace.conflict.hint') }}
        </p>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <NButton @click="((conflictDialogVisible = false), resetConflictState())">
            {{ t('workspace.common.cancel') }}
          </NButton>
          <NButton :loading="replacingResourceId !== null" @click="handleConflictReplace">
            {{ t('workspace.conflict.fullReplace') }}
          </NButton>
          <NButton type="primary" @click="handleConflictIncremental">
            {{ t('workspace.conflict.incrementalUpdate') }}
          </NButton>
        </div>
      </template>
    </NModal>

    <NModal
      v-model:show="incrementalResultVisible"
      preset="card"
      :title="t('workspace.incremental.resultTitle')"
      :style="{ width: '480px' }"
      :bordered="false"
      :mask-closable="false"
    >
      <div v-if="incrementalResult" class="grid grid-cols-2 gap-3">
        <div class="rounded-lg bg-emerald-50 p-4 text-center dark:bg-emerald-500/10">
          <div class="text-2xl font-bold text-emerald-600">
            {{ incrementalResult.changes.added }}
          </div>
          <div class="mt-1 text-xs text-emerald-600/70">
            {{ t('workspace.incremental.added') }}
          </div>
        </div>
        <div class="rounded-lg bg-blue-50 p-4 text-center dark:bg-blue-500/10">
          <div class="text-2xl font-bold text-blue-600">
            {{ incrementalResult.changes.updated }}
          </div>
          <div class="mt-1 text-xs text-blue-600/70">
            {{ t('workspace.incremental.updated') }}
          </div>
        </div>
        <div class="rounded-lg bg-gray-50 p-4 text-center dark:bg-gray-500/10">
          <div class="text-2xl font-bold text-gray-600">
            {{ incrementalResult.changes.unchanged }}
          </div>
          <div class="mt-1 text-xs text-gray-600/70">
            {{ t('workspace.incremental.unchanged') }}
          </div>
        </div>
        <div class="rounded-lg bg-red-50 p-4 text-center dark:bg-red-500/10">
          <div class="text-2xl font-bold text-red-600">
            {{ incrementalResult.changes.deleted }}
          </div>
          <div class="mt-1 text-xs text-red-600/70">
            {{ t('workspace.incremental.deleted') }}
          </div>
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end">
          <NButton type="primary" @click="confirmIncrementalResult">
            {{ t('workspace.common.confirm') }}
          </NButton>
        </div>
      </template>
    </NModal>

    <!-- 术语表新增/编辑抽屉 -->
    <NDrawer v-model:show="glossaryDrawerVisible" :width="480" placement="right">
      <NDrawerContent :title="glossaryDrawerTitle" closable>
        <NAlert
          v-if="isGlossaryEditMode ? glossary.updateError : glossary.createError"
          type="error"
          :bordered="false"
          class="mb-4"
        >
          {{ isGlossaryEditMode ? glossary.updateError : glossary.createError }}
        </NAlert>
        <NForm
          ref="glossaryFormRef"
          :model="glossaryForm"
          :rules="glossaryRules"
          label-placement="top"
        >
          <NFormItem :label="t('workspace.glossary.form.source')" path="source">
            <NInput
              v-model:value="glossaryForm.source"
              :placeholder="t('workspace.glossary.form.sourcePlaceholder')"
            />
          </NFormItem>
          <NFormItem :label="t('workspace.glossary.form.target')" path="target">
            <NInput
              v-model:value="glossaryForm.target"
              :placeholder="t('workspace.glossary.form.targetPlaceholder')"
            />
          </NFormItem>
          <NFormItem :label="t('workspace.glossary.form.caseSensitive')">
            <NCheckbox v-model:checked="glossaryForm.case_sensitive">
              {{ t('workspace.glossary.form.caseSensitive') }}
            </NCheckbox>
          </NFormItem>
          <NFormItem :label="t('workspace.glossary.form.notes')">
            <NInput
              v-model:value="glossaryForm.notes"
              type="textarea"
              :autosize="{ minRows: 2, maxRows: 4 }"
              :placeholder="t('workspace.glossary.form.notesPlaceholder')"
            />
          </NFormItem>
        </NForm>
        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton
              :disabled="glossary.creating || glossary.updating"
              @click="closeGlossaryDrawer"
            >
              {{ t('workspace.common.cancel') }}
            </NButton>
            <NButton
              type="primary"
              :loading="isGlossaryEditMode ? glossary.updating : glossary.creating"
              @click="submitGlossaryEntry"
            >
              {{ t('workspace.common.save') }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>

    <!-- 术语表导入弹窗 -->
    <NModal
      v-model:show="glossaryImportVisible"
      preset="card"
      :title="t('workspace.glossary.import.title')"
      :style="{ width: '480px' }"
      :bordered="false"
      :mask-closable="false"
    >
      <div class="space-y-4">
        <p class="text-sm text-lf-text-muted">
          {{ t('workspace.glossary.import.description') }}
        </p>
        <NUpload
          :max="1"
          accept=".csv"
          :default-upload="false"
          @change="
            (options: { file: UploadFileInfo }) => {
              if (options.file.file) {
                handleGlossaryImport(options.file.file)
              }
            }
          "
        >
          <NButton :loading="glossary.importing">
            <template #icon>
              <NIcon><IconCarbonUpload /></NIcon>
            </template>
            {{ t('workspace.glossary.actions.import') }}
          </NButton>
        </NUpload>
        <NAlert v-if="glossary.importResult" type="success" :bordered="false">
          {{ t('workspace.glossary.import.result', { added: glossary.importResult.added }) }}
          <template v-if="glossary.importResult.skipped?.length">
            ，{{
              t('workspace.glossary.import.skipped', {
                count: glossary.importResult.skipped.length,
              })
            }}
          </template>
        </NAlert>
      </div>
      <template #footer>
        <div class="flex justify-end">
          <NButton @click="glossaryImportVisible = false">
            {{ t('workspace.common.close') }}
          </NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>
