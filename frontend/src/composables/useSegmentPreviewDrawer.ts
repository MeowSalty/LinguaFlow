import { computed, onMounted, onUnmounted, ref, shallowRef, type Ref } from 'vue'
import { useDialog, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import {
  applyResourceSegmentTranslationPreview,
  isSegmentTranslationPreviewError,
} from '@/api/projects'
import type { ApiSchemas } from '@/api/client'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'

type Segment = ApiSchemas['Segment']

export type SegmentPreviewDrawerState =
  | 'idle'
  | 'previewing'
  | 'ready'
  | 'failed'
  | 'applying'
  | 'applied'

/** 翻译预览与修订预览响应中，抽屉共享逻辑所依赖的字段 */
export interface SegmentPreviewShared {
  status: 'success' | 'partial' | 'failed'
  source_text: string
  target_text?: string
  apply_token?: string
  apply_expires_at?: string
  quality_issues?: ApiSchemas['QualityIssue'][]
  batches: ApiSchemas['TranslationBatchDiagnostic'][]
  execution: { execution_plan_name: string; rounds: unknown[] }
}

export interface SegmentPreviewRequestArgs {
  projectId: number
  resourceId: number
  segmentId: number
  executionPlanId: number
  signal: AbortSignal
}

export interface SegmentPreviewDrawerOptions<TPreview extends SegmentPreviewShared> {
  /** v-model:show 的引用 */
  show: Ref<boolean>
  projectId: () => number | null
  /** 发起预览请求（翻译 / 修订各自注入具体 API 调用） */
  requestPreview: (args: SegmentPreviewRequestArgs) => Promise<TPreview>
  /** error 非 Error 实例时的兜底文案 i18n key */
  previewFallbackErrorKey: string
  /** 应用成功提示 i18n key */
  applySuccessKey: string
  /** 预览执行中关闭确认 dialog 的标题 / 内容 i18n key */
  closeTitleKey: string
  closeConfirmKey: string
  /** 应用成功后的回调（转发 emit applied） */
  onApplied: (payload: { segment: Segment; resourceId: number }) => void
}

/**
 * 单段预览抽屉（试译 / 修订）的共享状态机与流程：
 * idle → previewing → ready/failed → applying → applied，
 * 含 429/409/410 错误分支、apply_token 过期轮询、更换执行计划后的 stale 检测与关闭确认。
 */
export function useSegmentPreviewDrawer<TPreview extends SegmentPreviewShared>(
  options: SegmentPreviewDrawerOptions<TPreview>,
) {
  const dialog = useDialog()
  const message = useMessage()
  const { t } = useI18n()
  const templatesStore = useExecutionPlanTemplatesStore()

  const state = ref<SegmentPreviewDrawerState>('idle')
  const segment = shallowRef<Segment | null>(null)
  const resourceId = ref<number | null>(null)
  const preview = shallowRef<TPreview | null>(null)
  const selectedPlanId = ref<number | null>(null)
  const stalePlan = ref(false)
  const errorMessage = ref<string | null>(null)
  const errorStatus = ref<number | null>(null)
  const retryAfterSeconds = ref<number | null>(null)
  const draftTargetText = ref('')
  const appliedSegment = shallowRef<Segment | null>(null)
  const now = ref(Date.now())
  const requestSequence = ref(0)
  let previewController: AbortController | null = null
  let timer: ReturnType<typeof setInterval> | null = null

  const hasTarget = computed(() => Boolean(draftTargetText.value.trim()))
  const hasToken = computed(() => Boolean(preview.value?.apply_token))
  const tokenExpired = computed(() => {
    const expiresAt = preview.value?.apply_expires_at
    return Boolean(expiresAt && new Date(expiresAt).getTime() <= now.value)
  })
  const canApply = computed(() =>
    Boolean(
      options.projectId() &&
      resourceId.value &&
      segment.value &&
      hasTarget.value &&
      hasToken.value &&
      !tokenExpired.value &&
      !stalePlan.value &&
      (preview.value?.status === 'success' || preview.value?.status === 'partial') &&
      state.value !== 'previewing' &&
      state.value !== 'applying' &&
      state.value !== 'applied',
    ),
  )
  const busy = computed(() => state.value === 'previewing' || state.value === 'applying')
  const currentTargetText = computed(
    () => appliedSegment.value?.target_text ?? segment.value?.target_text,
  )
  const sourceText = computed(() => preview.value?.source_text ?? segment.value?.source_text ?? '')
  const executionSummary = computed(() => {
    const plan = preview.value?.execution
    if (!plan) return ''
    return t('workspace.segment.translationPreview.executionSummary', {
      name: plan.execution_plan_name,
      rounds: plan.rounds.length,
    })
  })

  const clearPreview = (): void => {
    preview.value = null
    errorMessage.value = null
    errorStatus.value = null
    retryAfterSeconds.value = null
    draftTargetText.value = ''
    stalePlan.value = false
    appliedSegment.value = null
    state.value = 'idle'
  }

  const open = (nextSegment: Segment, nextResourceId: number): void => {
    previewController?.abort()
    requestSequence.value++
    segment.value = { ...nextSegment }
    resourceId.value = nextResourceId
    selectedPlanId.value = null
    clearPreview()
    options.show.value = true
    if (templatesStore.items.length === 0) {
      void templatesStore.loadTemplates()
    }
  }

  const handlePlanChange = (value: number | null): void => {
    selectedPlanId.value = value
    if (preview.value) {
      stalePlan.value = true
    }
  }

  const startPreview = async (): Promise<void> => {
    const projectId = options.projectId()
    if (!projectId || !resourceId.value || !segment.value || !selectedPlanId.value) return

    previewController?.abort()
    const controller = new AbortController()
    previewController = controller
    const sequence = ++requestSequence.value
    state.value = 'previewing'
    errorMessage.value = null
    errorStatus.value = null
    retryAfterSeconds.value = null
    stalePlan.value = false
    appliedSegment.value = null

    try {
      const result = await options.requestPreview({
        projectId,
        resourceId: resourceId.value,
        segmentId: segment.value.id,
        executionPlanId: selectedPlanId.value,
        signal: controller.signal,
      })
      if (sequence !== requestSequence.value || controller.signal.aborted) return
      preview.value = result
      draftTargetText.value = result.target_text ?? ''
      state.value = result.status === 'failed' ? 'failed' : 'ready'
    } catch (error) {
      if (sequence !== requestSequence.value || controller.signal.aborted) return
      errorStatus.value = isSegmentTranslationPreviewError(error) ? error.status : null
      retryAfterSeconds.value = isSegmentTranslationPreviewError(error)
        ? (error.retryAfterSeconds ?? null)
        : null
      errorMessage.value =
        error instanceof Error ? error.message : t(options.previewFallbackErrorKey)
      state.value = 'failed'
    } finally {
      if (sequence === requestSequence.value) {
        previewController = null
      }
    }
  }

  const handleApply = async (): Promise<void> => {
    const projectId = options.projectId()
    if (
      !canApply.value ||
      !projectId ||
      !resourceId.value ||
      !segment.value ||
      !preview.value?.apply_token
    ) {
      return
    }

    state.value = 'applying'
    errorMessage.value = null
    try {
      const result = await applyResourceSegmentTranslationPreview(
        projectId,
        resourceId.value,
        segment.value.id,
        preview.value.apply_token,
        draftTargetText.value,
      )
      appliedSegment.value = result
      state.value = 'applied'
      message.success(t(options.applySuccessKey))
      options.onApplied({ segment: result, resourceId: resourceId.value })
    } catch (error) {
      const knownError = isSegmentTranslationPreviewError(error) ? error : null
      errorStatus.value = knownError?.status ?? null
      errorMessage.value =
        error instanceof Error
          ? error.message
          : t('api.errors.applySegmentTranslationPreviewFailed')
      if (knownError?.status === 409 || knownError?.status === 410) {
        preview.value = preview.value ? { ...preview.value, apply_token: undefined } : null
      }
      state.value = 'ready'
    }
  }

  const close = (): void => {
    options.show.value = false
    previewController?.abort()
    previewController = null
    requestSequence.value++
  }

  const requestClose = (): void => {
    if (state.value === 'applying') return
    if (state.value !== 'previewing') {
      close()
      return
    }

    dialog.warning({
      title: t(options.closeTitleKey),
      content: t(options.closeConfirmKey),
      positiveText: t('common.confirm'),
      negativeText: t('common.cancel'),
      onPositiveClick: () => close(),
    })
  }

  onMounted(() => {
    timer = setInterval(() => {
      now.value = Date.now()
    }, 1000)
  })

  onUnmounted(() => {
    previewController?.abort()
    if (timer) clearInterval(timer)
  })

  return {
    // 状态
    state,
    segment,
    resourceId,
    preview,
    selectedPlanId,
    stalePlan,
    errorMessage,
    errorStatus,
    retryAfterSeconds,
    draftTargetText,
    appliedSegment,
    // 计算属性
    canApply,
    busy,
    tokenExpired,
    currentTargetText,
    sourceText,
    executionSummary,
    // 方法
    open,
    handlePlanChange,
    startPreview,
    handleApply,
    requestClose,
  }
}
