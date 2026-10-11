import { computed, onScopeDispose, reactive, ref, watch, type Ref } from 'vue'
import type { FormInst, FormRules } from 'naive-ui'
import { useMessage } from 'naive-ui'

import { type ApiSchemas } from '@/api/client'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { ApiError, isAccessDenied } from '@/api/utils'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'
import { isOrganizationDependency } from '@/utils/organization-scope'
import { isJobActionAllowed, isJobTerminal, type JobAction } from '@/utils/jobPresentation'

type Job = ApiSchemas['Job']
type CreateJobRequest = ApiSchemas['CreateJobRequest']
type SegmentFilter = NonNullable<CreateJobRequest['segment_filter']>

export type JobTargetMode = 'resources' | 'segments'

export interface JobFormModel {
  execution_plan_id: number | null
  auto_approve: boolean
  segment_filter: SegmentFilter | undefined
}

export function useJobActions(projectId: Ref<number | null>, onJobCreated?: () => Promise<void>) {
  const message = useMessage()
  const workspace = useProjectWorkspaceStore()
  const globalTracker = useGlobalJobTrackerStore()
  const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()
  let disposed = false
  let viewGeneration = 0
  const captureView = () => {
    const session = captureSession()
    const id = projectId.value
    const generation = viewGeneration
    return () =>
      !disposed &&
      isSessionCurrent(session) &&
      projectId.value === id &&
      generation === viewGeneration
  }
  const availablePlans = computed(() =>
    executionPlanTemplatesStore.items.filter((item) =>
      isOrganizationDependency(item, workspace.project?.owner_org_id ?? null),
    ),
  )

  // ── 状态 ──
  const jobDrawerVisible = ref(false)
  const jobFormRef = ref<FormInst | null>(null)
  const jobTargetMode = ref<JobTargetMode>('resources')
  const jobTargetResourceIds = ref<number[]>([])
  const jobTargetSegmentIds = ref<number[]>([])
  const jobTargetGroupKeys = ref<string[]>([])

  const jobForm = reactive<JobFormModel>({
    execution_plan_id: null,
    auto_approve: false,
    segment_filter: undefined,
  })

  // ── 计算属性 ──
  const executionPlanOptions = computed(() =>
    availablePlans.value.map((item) => ({
      label: t('workspace.job.executionPlanLabel', {
        name: item.name,
        rounds: item.rounds?.length ?? 0,
      }),
      value: item.id,
    })),
  )

  const selectedPlanTemplate = computed(
    () => availablePlans.value.find((item) => item.id === jobForm.execution_plan_id) ?? null,
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

  const canCreateResourceJob = computed(() => workspace.selectedResourceIds.length > 0)
  // ── 方法 ──
  const clearResourceSelection = (): void => {
    workspace.clearSelectedResources()
  }
  const openResourceJobDrawer = (): void => {
    if (!canCreateResourceJob.value) {
      message.warning(t('workspace.messages.selectReadyResource'))
      return
    }

    jobTargetMode.value = 'resources'
    jobTargetResourceIds.value = [...workspace.selectedResourceIds]
    jobTargetSegmentIds.value = []
    jobTargetGroupKeys.value = []
    jobForm.execution_plan_id = null
    jobForm.segment_filter = undefined
    jobDrawerVisible.value = true
  }

  /** 使用指定的资源 ID 列表打开任务创建抽屉（用于 EPUB 章节翻译等场景） */
  const openResourceJobDrawerWithIds = (resourceIds: number[], groupKeys?: string[]): void => {
    if (resourceIds.length === 0) {
      message.warning(t('workspace.messages.selectReadyResource'))
      return
    }

    jobTargetMode.value = 'resources'
    jobTargetResourceIds.value = [...resourceIds]
    jobTargetSegmentIds.value = []
    jobTargetGroupKeys.value = groupKeys ? [...groupKeys] : []
    jobForm.execution_plan_id = null
    jobForm.segment_filter = undefined
    jobDrawerVisible.value = true
  }

  const openSegmentJobDrawerWithIds = (segmentIds: number[]): void => {
    if (!workspace.activeResourceId) {
      message.warning(t('workspace.messages.selectResourceFirst'))
      return
    }

    if (segmentIds.length === 0) {
      message.warning(t('workspace.messages.selectReadyResource'))
      return
    }

    jobTargetMode.value = 'segments'
    jobTargetResourceIds.value = [workspace.activeResourceId]
    jobTargetSegmentIds.value = segmentIds
    jobTargetGroupKeys.value = []
    jobForm.execution_plan_id = null
    jobForm.segment_filter = undefined
    jobDrawerVisible.value = true
  }

  const closeJobDrawer = (): void => {
    jobDrawerVisible.value = false
    jobTargetResourceIds.value = []
    jobTargetSegmentIds.value = []
    jobTargetGroupKeys.value = []
    jobForm.execution_plan_id = null
    jobForm.auto_approve = false
    jobForm.segment_filter = undefined
  }

  const submitJob = async (): Promise<boolean> => {
    if (!projectId.value || !jobForm.execution_plan_id || workspace.creatingJob) {
      return false
    }
    if (!availablePlans.value.some((item) => item.id === jobForm.execution_plan_id)) {
      message.error(t('team.errors.dependencies'))
      return false
    }

    const current = captureView()
    const submittedProjectId = projectId.value
    const payload: CreateJobRequest = {
      execution_plan_id: jobForm.execution_plan_id,
      resource_ids: [...jobTargetResourceIds.value],
      auto_approve: jobForm.auto_approve,
    }

    if (jobForm.segment_filter) {
      payload.segment_filter = jobForm.segment_filter
    }

    if (jobTargetGroupKeys.value.length > 0) {
      payload.segment_group_keys = [...jobTargetGroupKeys.value]
    }

    if (jobTargetMode.value === 'segments') {
      payload.segment_ids = [...jobTargetSegmentIds.value]
    }

    try {
      const job = await workspace.createJob(submittedProjectId, payload)
      if (!current()) return false
      message.success(t('workspace.messages.jobCreated'))
      closeJobDrawer()

      globalTracker.trackJob(job, workspace.project?.name)

      if (onJobCreated) {
        try {
          await onJobCreated()
        } catch (error) {
          if (current())
            message.warning(
              error instanceof Error ? error.message : t('api.errors.fetchJobsFailed'),
            )
        }
      }
      return true
    } catch (error) {
      if (!current()) return false
      if (error instanceof ApiError && error.status === 409)
        message.warning(t('workbench.details.conflict'))
      else
        message.error(
          isAccessDenied(error)
            ? t('operations.inaccessible')
            : workspace.actionError || t('workspace.messages.jobCreateFailed'),
        )
      return false
    }
  }

  const actionHandlers: Record<JobAction, (id: number) => Promise<Job | void>> = {
    cancel: workspace.cancelJob,
    retry: workspace.retryJob,
    pause: workspace.pauseJob,
    resume: workspace.resumeJob,
  }
  const actionMessages = {
    cancel: ['workspace.messages.jobCancelled', 'workspace.messages.jobCancelFailed'],
    retry: ['workspace.messages.jobRetried', 'workspace.messages.jobRetryFailed'],
    pause: ['workspace.messages.jobPaused', 'workspace.messages.jobPauseFailed'],
    resume: ['workspace.messages.jobResumed', 'workspace.messages.jobResumeFailed'],
  } as const
  const runAction = async (action: JobAction, job: Job): Promise<void> => {
    if (
      job.project_id !== projectId.value ||
      !isJobActionAllowed(action, job.status) ||
      [
        ...workspace.cancellingJobIds,
        ...workspace.retryingJobIds,
        ...workspace.pausingJobIds,
        ...workspace.resumingJobIds,
      ].includes(job.id)
    )
      return
    const current = captureView()
    try {
      const response = await actionHandlers[action](job.id)
      if (!current()) return
      message.success(
        t(
          action === 'pause' && response && response.status !== 'paused'
            ? isJobTerminal(response.status)
              ? 'workbench.details.updated'
              : 'workbench.details.pauseRequested'
            : actionMessages[action][0],
        ),
      )
    } catch (error) {
      if (!current()) return
      if (isAccessDenied(error)) {
        if (globalTracker.drawerJobId === job.id) globalTracker.closeDetail()
        message.error(t('operations.inaccessible'))
      } else if (error instanceof ApiError && error.status === 409) {
        // The store has refreshed the list; update an open detail before showing the conflict.
        if (globalTracker.drawerJobId === job.id) await globalTracker.refreshDetail()
        if (current()) message.warning(t('workbench.details.conflict'))
      } else message.error(workspace.actionError || t(actionMessages[action][1]))
    }
  }
  const cancelJob = (job: Job): Promise<void> => runAction('cancel', job)
  const retryJob = (job: Job): Promise<void> => runAction('retry', job)
  const pauseJob = (job: Job): Promise<void> => runAction('pause', job)
  const resumeJob = (job: Job): Promise<void> => runAction('resume', job)

  const openJobDetail = async (job: Job): Promise<void> => {
    if (disposed || job.project_id !== projectId.value) return
    globalTracker.trackJob(job, workspace.project?.name)
    await globalTracker.openDetail(job.id)
  }

  watch(jobDrawerVisible, (show) => {
    if (show) void executionPlanTemplatesStore.loadTemplates(null)
  })
  watch(
    projectId,
    () => {
      viewGeneration++
      closeJobDrawer()
    },
    { flush: 'sync' },
  )
  onScopeDispose(
    onSessionChange(() => {
      viewGeneration++
      closeJobDrawer()
    }),
  )
  onScopeDispose(() => {
    disposed = true
    viewGeneration++
  })

  return {
    // 状态
    jobDrawerVisible,
    jobFormRef,
    jobTargetMode,
    jobTargetResourceIds,
    jobTargetSegmentIds,
    jobTargetGroupKeys,
    jobForm,
    // 计算属性
    executionPlanOptions,
    selectedPlanTemplate,
    jobFormRules,
    selectedResourceIds: computed(() => workspace.selectedResourceIds),
    canCreateResourceJob,
    // 方法
    openResourceJobDrawer,
    openResourceJobDrawerWithIds,
    openSegmentJobDrawerWithIds,
    closeJobDrawer,
    submitJob,
    cancelJob,
    retryJob,
    pauseJob,
    resumeJob,
    openJobDetail,
    clearResourceSelection,
  }
}
