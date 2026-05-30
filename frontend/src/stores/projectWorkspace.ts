import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  cancelTranslationJob as cancelTranslationJobRequest,
  createTranslationJob as createTranslationJobRequest,
  deleteProjectResource as deleteProjectResourceRequest,
  downloadProjectResource as downloadProjectResourceRequest,
  downloadTranslationJobResult as downloadTranslationJobResultRequest,
  type ApiSchemas,
  type DownloadFileResult,
  fetchProject,
  fetchProjectResources,
  fetchResourceSegments,
  fetchTranslationJob,
  fetchTranslationJobs,
  replaceProjectResource as replaceProjectResourceRequest,
  retryTranslationJob as retryTranslationJobRequest,
  updateResourceSegment as updateResourceSegmentRequest,
  uploadProjectResourcesWithProgress,
} from '@/api/client'
import { t } from '@/i18n'

type Project = ApiSchemas['Project']
type Resource = ApiSchemas['Resource']
type Segment = ApiSchemas['Segment']
type TranslationJob = ApiSchemas['TranslationJob']
type CreateTranslationJobPayload = ApiSchemas['CreateTranslationJobRequest']
type SegmentUpdatePayload = ApiSchemas['ResourceSegmentUpdateRequest']

export interface UploadTask {
  id: string
  fileName: string
  stage: 'uploading' | 'processing' | 'complete' | 'error'
  progress: number
  errorMessage?: string
}

export type ResourceStatusFilter = Resource['status'] | 'all'
export type SegmentStatusFilter = 'pending' | 'translated' | 'reviewed' | 'rejected' | 'all'
export type JobStatusFilter = TranslationJob['status'] | 'all'

const getErrorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error ? error.message : fallback

const upsertById = <T extends { id: number }>(items: T[], item: T): T[] => [
  item,
  ...items.filter((current) => current.id !== item.id),
]

export const useProjectWorkspaceStore = defineStore('projectWorkspace', () => {
  const project = ref<Project | null>(null)
  const resources = ref<Resource[]>([])
  const selectedResourceIds = ref<number[]>([])
  const activeResourceId = ref<number | null>(null)
  const segments = ref<Segment[]>([])
  const jobs = ref<TranslationJob[]>([])
  const selectedJob = ref<TranslationJob | null>(null)

  const resourcesCursor = ref<string | null>(null)
  const segmentsCursor = ref<string | null>(null)
  const jobsCursor = ref<string | null>(null)

  const loadingProject = ref(false)
  const loadingResources = ref(false)
  const loadingSegments = ref(false)
  const loadingJobs = ref(false)
  const loadingJobDetail = ref(false)
  const uploadTasks = ref<UploadTask[]>([])
  const replacingResourceIds = ref<number[]>([])
  const deletingResourceIds = ref<number[]>([])
  const editingSegmentIds = ref<number[]>([])
  const creatingJob = ref(false)
  const cancellingJobIds = ref<number[]>([])
  const retryingJobIds = ref<number[]>([])
  const downloadingKeys = ref<string[]>([])

  const projectError = ref<string | null>(null)
  const resourcesError = ref<string | null>(null)
  const segmentsError = ref<string | null>(null)
  const jobsError = ref<string | null>(null)
  const jobDetailError = ref<string | null>(null)
  const actionError = ref<string | null>(null)

  const resourceSearch = ref('')
  const resourceStatusFilter = ref<ResourceStatusFilter>('all')
  const resourceFormatFilter = ref<string>('all')
  const segmentSearch = ref('')
  const segmentStatusFilter = ref<SegmentStatusFilter>('all')
  const jobStatusFilter = ref<JobStatusFilter>('all')

  const activeResource = computed<Resource | null>(
    () => resources.value.find((resource) => resource.id === activeResourceId.value) ?? null,
  )
  const selectedResources = computed<Resource[]>(() =>
    resources.value.filter((resource) => selectedResourceIds.value.includes(resource.id)),
  )
  const availableFormats = computed<string[]>(() =>
    [...new Set(resources.value.map((resource) => resource.format).filter(Boolean))].sort(),
  )
  const readyResourceCount = computed(
    () => resources.value.filter((resource) => resource.status === 'ready').length,
  )
  const totalSegmentCount = computed(() =>
    resources.value.reduce((total, resource) => total + resource.total_segments, 0),
  )
  const runningJobCount = computed(
    () => jobs.value.filter((job) => job.status === 'pending' || job.status === 'running').length,
  )
  const hasActiveUploads = computed(() =>
    uploadTasks.value.some((task) => task.stage === 'uploading' || task.stage === 'processing'),
  )

  const loadProject = async (projectId: number): Promise<void> => {
    loadingProject.value = true
    projectError.value = null

    try {
      project.value = await fetchProject(projectId)
    } catch (error) {
      projectError.value = getErrorMessage(error, t('api.errors.fetchProjectFailed'))
    } finally {
      loadingProject.value = false
    }
  }

  const loadResources = async (projectId: number, append = false): Promise<void> => {
    loadingResources.value = true
    resourcesError.value = null

    try {
      const response = await fetchProjectResources(projectId, {
        status: resourceStatusFilter.value === 'all' ? undefined : resourceStatusFilter.value,
        format: resourceFormatFilter.value === 'all' ? undefined : resourceFormatFilter.value,
        search: resourceSearch.value.trim() || undefined,
        cursor: append ? (resourcesCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      resources.value = append ? [...resources.value, ...response.items] : response.items
      resourcesCursor.value = null

      if (!activeResourceId.value && resources.value[0]) {
        activeResourceId.value = resources.value[0].id
      }

      selectedResourceIds.value = selectedResourceIds.value.filter((id) =>
        resources.value.some((resource) => resource.id === id),
      )
      if (
        activeResourceId.value &&
        !resources.value.some((item) => item.id === activeResourceId.value)
      ) {
        activeResourceId.value = resources.value[0]?.id ?? null
      }
    } catch (error) {
      resourcesError.value = getErrorMessage(error, t('api.errors.fetchResourcesFailed'))
    } finally {
      loadingResources.value = false
    }
  }

  const loadSegments = async (
    projectId: number,
    resourceId: number,
    append = false,
  ): Promise<void> => {
    loadingSegments.value = true
    segmentsError.value = null

    try {
      const response = await fetchResourceSegments(projectId, resourceId, {
        status: segmentStatusFilter.value === 'all' ? undefined : segmentStatusFilter.value,
        search: segmentSearch.value.trim() || undefined,
        cursor: append ? (segmentsCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      segments.value = append ? [...segments.value, ...response.items] : response.items
      segmentsCursor.value = response.next_cursor ?? null
    } catch (error) {
      segmentsError.value = getErrorMessage(error, t('api.errors.fetchSegmentsFailed'))
    } finally {
      loadingSegments.value = false
    }
  }

  const loadJobs = async (projectId: number, append = false): Promise<void> => {
    loadingJobs.value = true
    jobsError.value = null

    try {
      const response = await fetchTranslationJobs(projectId, {
        status: jobStatusFilter.value === 'all' ? undefined : jobStatusFilter.value,
        cursor: append ? (jobsCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      jobs.value = append ? [...jobs.value, ...response.items] : response.items
      jobsCursor.value = response.next_cursor ?? null
    } catch (error) {
      jobsError.value = getErrorMessage(error, t('api.errors.fetchTranslationJobsFailed'))
    } finally {
      loadingJobs.value = false
    }
  }

  const loadJobDetail = async (translationJobId: number): Promise<TranslationJob> => {
    loadingJobDetail.value = true
    jobDetailError.value = null

    try {
      const job = await fetchTranslationJob(translationJobId)
      selectedJob.value = job
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
      return job
    } catch (error) {
      jobDetailError.value = getErrorMessage(error, t('api.errors.fetchTranslationJobFailed'))
      throw error
    } finally {
      loadingJobDetail.value = false
    }
  }

  const addUploadTask = (fileName: string): string => {
    const id = crypto.randomUUID()
    uploadTasks.value = [...uploadTasks.value, { id, fileName, stage: 'uploading', progress: 0 }]
    return id
  }

  const updateUploadTaskProgress = (taskId: string, progress: number): void => {
    uploadTasks.value = uploadTasks.value.map((task) =>
      task.id === taskId ? { ...task, progress } : task,
    )
  }

  const updateUploadTaskStage = (
    taskId: string,
    stage: UploadTask['stage'],
    errorMessage?: string,
  ): void => {
    uploadTasks.value = uploadTasks.value.map((task) =>
      task.id === taskId ? { ...task, stage, errorMessage } : task,
    )
  }

  const removeUploadTask = (taskId: string): void => {
    uploadTasks.value = uploadTasks.value.filter((task) => task.id !== taskId)
  }

  const clearCompletedUploadTasks = (): void => {
    uploadTasks.value = uploadTasks.value.filter((task) => task.stage !== 'complete')
  }

  const clearAllUploadTasks = (): void => {
    uploadTasks.value = []
  }

  const uploadResources = async (
    projectId: number,
    files: File[],
    taskId?: string,
  ): Promise<void> => {
    if (files.length === 0) {
      return
    }

    actionError.value = null

    try {
      const response = await uploadProjectResourcesWithProgress(projectId, files, {
        onProgress: (percent) => {
          if (taskId) {
            updateUploadTaskProgress(taskId, percent)
          }
        },
        onServerProcessing: () => {
          if (taskId) {
            updateUploadTaskStage(taskId, 'processing')
          }
        },
      })
      resources.value = [...response.items, ...resources.value]
      if (!activeResourceId.value && response.items[0]) {
        activeResourceId.value = response.items[0].id
      }
    } catch (error) {
      const message = getErrorMessage(error, t('api.errors.uploadResourcesFailed'))
      actionError.value = message
      if (taskId) {
        updateUploadTaskStage(taskId, 'error', message)
      }
      throw error
    }
  }

  const replaceResource = async (
    projectId: number,
    resourceId: number,
    file: File,
  ): Promise<void> => {
    replacingResourceIds.value = [...replacingResourceIds.value, resourceId]
    actionError.value = null

    try {
      const resource = await replaceProjectResourceRequest(projectId, resourceId, file)
      resources.value = resources.value.map((item) => (item.id === resource.id ? resource : item))
      if (activeResourceId.value === resourceId) {
        segments.value = []
        segmentsCursor.value = null
      }
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.replaceResourceFailed'))
      throw error
    } finally {
      replacingResourceIds.value = replacingResourceIds.value.filter((id) => id !== resourceId)
    }
  }

  const deleteResource = async (projectId: number, resourceId: number): Promise<void> => {
    deletingResourceIds.value = [...deletingResourceIds.value, resourceId]
    actionError.value = null

    try {
      await deleteProjectResourceRequest(projectId, resourceId)
      resources.value = resources.value.filter((resource) => resource.id !== resourceId)
      selectedResourceIds.value = selectedResourceIds.value.filter((id) => id !== resourceId)
      if (activeResourceId.value === resourceId) {
        activeResourceId.value = resources.value[0]?.id ?? null
        segments.value = []
        segmentsCursor.value = null
      }
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.deleteResourceFailed'))
      throw error
    } finally {
      deletingResourceIds.value = deletingResourceIds.value.filter((id) => id !== resourceId)
    }
  }

  const updateSegment = async (
    projectId: number,
    resourceId: number,
    segmentId: number,
    payload: SegmentUpdatePayload,
  ): Promise<Segment> => {
    editingSegmentIds.value = [...editingSegmentIds.value, segmentId]
    actionError.value = null

    try {
      const segment = await updateResourceSegmentRequest(projectId, resourceId, segmentId, payload)
      segments.value = segments.value.map((item) => (item.id === segment.id ? segment : item))
      return segment
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.updateSegmentFailed'))
      throw error
    } finally {
      editingSegmentIds.value = editingSegmentIds.value.filter((id) => id !== segmentId)
    }
  }

  const createJob = async (
    projectId: number,
    payload: CreateTranslationJobPayload,
  ): Promise<TranslationJob> => {
    creatingJob.value = true
    actionError.value = null

    try {
      const job = await createTranslationJobRequest(projectId, payload)
      jobs.value = upsertById(jobs.value, job)
      return job
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.createTranslationJobFailed'))
      throw error
    } finally {
      creatingJob.value = false
    }
  }

  const cancelJob = async (translationJobId: number): Promise<void> => {
    cancellingJobIds.value = [...cancellingJobIds.value, translationJobId]
    actionError.value = null

    try {
      const job = await cancelTranslationJobRequest(translationJobId)
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.cancelTranslationJobFailed'))
      throw error
    } finally {
      cancellingJobIds.value = cancellingJobIds.value.filter((id) => id !== translationJobId)
    }
  }

  const retryJob = async (translationJobId: number): Promise<void> => {
    retryingJobIds.value = [...retryingJobIds.value, translationJobId]
    actionError.value = null

    try {
      const job = await retryTranslationJobRequest(translationJobId)
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.retryTranslationJobFailed'))
      throw error
    } finally {
      retryingJobIds.value = retryingJobIds.value.filter((id) => id !== translationJobId)
    }
  }

  const downloadResource = async (
    projectId: number,
    resourceId: number,
  ): Promise<DownloadFileResult> => {
    const key = `resource:${resourceId}`
    downloadingKeys.value = [...downloadingKeys.value, key]
    actionError.value = null

    try {
      return await downloadProjectResourceRequest(projectId, resourceId)
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.downloadResourceFailed'))
      throw error
    } finally {
      downloadingKeys.value = downloadingKeys.value.filter((item) => item !== key)
    }
  }

  const downloadJobResult = async (
    translationJobId: number,
    resourceId?: number,
  ): Promise<DownloadFileResult> => {
    const key = `job:${translationJobId}:${resourceId ?? 'all'}`
    downloadingKeys.value = [...downloadingKeys.value, key]
    actionError.value = null

    try {
      return await downloadTranslationJobResultRequest(translationJobId, resourceId)
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.downloadTranslationJobFailed'))
      throw error
    } finally {
      downloadingKeys.value = downloadingKeys.value.filter((item) => item !== key)
    }
  }

  const setActiveResource = (resourceId: number | null): void => {
    activeResourceId.value = resourceId
    segments.value = []
    segmentsCursor.value = null
  }

  const reset = (): void => {
    project.value = null
    resources.value = []
    selectedResourceIds.value = []
    activeResourceId.value = null
    segments.value = []
    jobs.value = []
    selectedJob.value = null
    resourcesCursor.value = null
    segmentsCursor.value = null
    jobsCursor.value = null
    projectError.value = null
    resourcesError.value = null
    segmentsError.value = null
    jobsError.value = null
    jobDetailError.value = null
    actionError.value = null
    clearAllUploadTasks()
    resourceSearch.value = ''
    resourceStatusFilter.value = 'all'
    resourceFormatFilter.value = 'all'
    segmentSearch.value = ''
    segmentStatusFilter.value = 'all'
    jobStatusFilter.value = 'all'
  }

  return {
    project,
    resources,
    selectedResourceIds,
    activeResourceId,
    activeResource,
    selectedResources,
    segments,
    jobs,
    selectedJob,
    resourcesCursor,
    segmentsCursor,
    jobsCursor,
    loadingProject,
    loadingResources,
    loadingSegments,
    loadingJobs,
    loadingJobDetail,
    uploadTasks,
    hasActiveUploads,
    replacingResourceIds,
    deletingResourceIds,
    editingSegmentIds,
    creatingJob,
    cancellingJobIds,
    retryingJobIds,
    downloadingKeys,
    projectError,
    resourcesError,
    segmentsError,
    jobsError,
    jobDetailError,
    actionError,
    resourceSearch,
    resourceStatusFilter,
    resourceFormatFilter,
    segmentSearch,
    segmentStatusFilter,
    jobStatusFilter,
    availableFormats,
    readyResourceCount,
    totalSegmentCount,
    runningJobCount,
    loadProject,
    loadResources,
    loadSegments,
    loadJobs,
    loadJobDetail,
    addUploadTask,
    updateUploadTaskProgress,
    updateUploadTaskStage,
    removeUploadTask,
    clearCompletedUploadTasks,
    clearAllUploadTasks,
    uploadResources,
    replaceResource,
    deleteResource,
    updateSegment,
    createJob,
    cancelJob,
    retryJob,
    downloadResource,
    downloadJobResult,
    setActiveResource,
    reset,
  }
})
