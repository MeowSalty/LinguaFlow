import { computed, reactive, ref, type Ref } from 'vue'
import type { FormInst, FormRules } from 'naive-ui'
import { useMessage } from 'naive-ui'

import { type ApiSchemas } from '@/api/client'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'

type Segment = ApiSchemas['Segment']
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
  const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()

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
    executionPlanTemplatesStore.items.map((item) => ({
      label: t('workspace.job.executionPlanLabel', {
        name: item.name,
        rounds: item.rounds?.length ?? 0,
      }),
      value: item.id,
    })),
  )

  const selectedPlanTemplate = computed(
    () =>
      executionPlanTemplatesStore.items.find((item) => item.id === jobForm.execution_plan_id) ??
      null,
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
  const canCreateSegmentJob = computed(() => workspace.segments.length > 0)

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
    console.debug('[useJobActions] openResourceJobDrawerWithIds:', {
      resourceIds: [...resourceIds],
      groupKeys: groupKeys ? [...groupKeys] : [],
      jobTargetGroupKeys: [...jobTargetGroupKeys.value],
    })
    jobForm.execution_plan_id = null
    jobForm.segment_filter = undefined
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

  const submitJob = async (): Promise<void> => {
    if (!projectId.value || !jobForm.execution_plan_id) {
      return
    }

    const payload: CreateJobRequest = {
      execution_plan_id: jobForm.execution_plan_id,
      resource_ids: jobTargetResourceIds.value,
      auto_approve: jobForm.auto_approve,
    }

    if (jobForm.segment_filter) {
      payload.segment_filter = jobForm.segment_filter
    }

    if (jobTargetGroupKeys.value.length > 0) {
      payload.segment_group_keys = jobTargetGroupKeys.value
    }

    if (jobTargetMode.value === 'segments') {
      payload.segment_ids = jobTargetSegmentIds.value
    }

    console.debug('[useJobActions] submitJob payload:', {
      targetMode: jobTargetMode.value,
      resourceIds: [...jobTargetResourceIds.value],
      groupKeys: [...jobTargetGroupKeys.value],
      segmentIds: [...jobTargetSegmentIds.value],
      payloadGroupKeys: payload.segment_group_keys ? [...payload.segment_group_keys] : undefined,
      payloadSegmentIds: payload.segment_ids ? [...payload.segment_ids] : undefined,
    })

    try {
      const job = await workspace.createJob(projectId.value, payload)
      message.success(t('workspace.messages.jobCreated'))
      closeJobDrawer()

      const globalTracker = useGlobalJobTrackerStore()
      globalTracker.trackJob(job, workspace.project?.name)

      if (onJobCreated) {
        await onJobCreated()
      }
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.jobCreateFailed'))
    }
  }

  const cancelJob = async (job: Job): Promise<void> => {
    try {
      await workspace.cancelJob(job.id)
      message.success(t('workspace.messages.jobCancelled'))
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.jobCancelFailed'))
    }
  }

  const retryJob = async (job: Job): Promise<void> => {
    try {
      await workspace.retryJob(job.id)
      message.success(t('workspace.messages.jobRetried'))
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.jobRetryFailed'))
    }
  }

  const openJobDetail = async (job: Job): Promise<void> => {
    const globalTracker = useGlobalJobTrackerStore()
    globalTracker.trackJob(job, workspace.project?.name)
    await globalTracker.openDetail(job.id)
  }

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
    canCreateSegmentJob,
    // 方法
    openResourceJobDrawer,
    openResourceJobDrawerWithIds,
    openSegmentJobDrawer,
    openSegmentJobDrawerWithIds,
    closeJobDrawer,
    submitJob,
    cancelJob,
    retryJob,
    openJobDetail,
    clearResourceSelection,
  }
}
